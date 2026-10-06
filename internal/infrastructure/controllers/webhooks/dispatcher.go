package webhooks

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	registry "github.com/rios0rios0/gitforge/v4/pkg/registry/infrastructure"
	logger "github.com/sirupsen/logrus"

	"github.com/rios0rios0/codeguru/internal/domain/commands"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/domain/repositories"
	infraRepos "github.com/rios0rios0/codeguru/internal/infrastructure/repositories"
)

// webhookDedupTTL bounds how long a `(provider, repo_id, pr_id)`
// triple suppresses a duplicate webhook delivery. ADO routinely
// double-fires `pullrequest.created` + `pullrequest.updated` for new
// PRs (verified live across `#NNNN`, `#NNNN`, `#NNNN` on
// `2026-05-01`); the longest observed gap between sibling deliveries
// was 4 seconds. 30 s gives headroom for ADO retry storms while
// staying far below any realistic real-follow-up-push interval (which
// is minutes, not seconds), so a real new commit never gets dedup'd.
const webhookDedupTTL = 30 * time.Second

// Submitter is the subset of *Pool used by handlers; defining it here lets tests
// substitute the pool without spinning up real workers.
type Submitter interface {
	Submit(job Job) error
}

// Dispatcher bridges webhook events to the domain review logic.
type Dispatcher struct {
	aiFactory             *infraRepos.AIReviewerFactory
	rulesFactory          *infraRepos.RulesRepositoryFactory
	detectorRegistry      repositories.TrivialDetectorRegistry
	metadataRepo          repositories.PullRequestMetadataRepository
	settings              *entities.Settings
	providerRegistry      *registry.ProviderRegistry
	submitter             Submitter
	githubTokenizer       GitHubTokenizer
	adoHydrator           ADOResourceHydrator
	adoIdentityResolver   ADOIdentityResolver
	dedup                 WebhookDedup
	allowedSourcePrefixes []netip.Prefix

	// inFlightMu guards inFlight, the set of dedup keys currently held
	// by an in-flight worker. The serve controller's pool handler
	// inserts on `dedupSeen` success and removes on `ReleaseDedup`; a
	// SIGTERM-time `ReleaseAllInFlight` walks the set so leases for jobs
	// the drain timeout cancelled are explicitly released instead of
	// orphaned in etcd. Without this set every rollout leaks one lease
	// per job that the 90 s drain budget could not finish — captured live
	// at 2026-05-02T00:18Z where four orphaned leases blocked an internal PR
	// reviews for 12 minutes after a routine `kubectl rollout restart`.
	inFlightMu sync.Mutex
	inFlight   map[string]struct{}
}

// GitHubTokenizer resolves an installation access token for a GitHub App.
// Production code uses the JWT-based exchanger; tests can substitute a stub.
type GitHubTokenizer interface {
	InstallationToken(ctx context.Context, installationID int64) (string, error)
}

// NewDispatcher creates a new webhook dispatcher.
func NewDispatcher(
	aiFactory *infraRepos.AIReviewerFactory,
	rulesFactory *infraRepos.RulesRepositoryFactory,
	detectorRegistry repositories.TrivialDetectorRegistry,
	metadataRepo repositories.PullRequestMetadataRepository,
	settings *entities.Settings,
	providerRegistry *registry.ProviderRegistry,
) *Dispatcher {
	return &Dispatcher{
		aiFactory:             aiFactory,
		rulesFactory:          rulesFactory,
		detectorRegistry:      detectorRegistry,
		metadataRepo:          metadataRepo,
		settings:              settings,
		providerRegistry:      providerRegistry,
		adoHydrator:           NewHTTPADOHydrator(nil),
		adoIdentityResolver:   NewHTTPADOIdentityResolver(nil),
		dedup:                 newInMemoryDedup(webhookDedupTTL),
		allowedSourcePrefixes: parseAllowedCIDRs(settings.Server.AllowedSourceCIDRs),
		inFlight:              map[string]struct{}{},
	}
}

// dedupSeen returns true when a webhook delivery for the same
// caller-supplied key has already been processed inside the dedup
// window — the caller should short-circuit in that case. Each
// handler picks its own key shape (ADO uses
// `ado:<repo_uuid>:<pr_id>`; GitHub uses
// `gh:<owner>/<repo>:<pr_id>`), so the dedup backend itself stays
// provider-agnostic. Hides the nil-check so handler code stays
// linear; the nil branch only matters in tests that build a
// `Dispatcher` without the constructor.
//
// `ctx` is forwarded to the backend so the K8s-Lease implementation
// can bound its API-server calls; the in-memory backend ignores it.
func (d *Dispatcher) dedupSeen(ctx context.Context, key string) bool {
	if d.dedup == nil {
		return false
	}
	if d.dedup.SeenRecently(ctx, key) {
		return true
	}
	d.trackInFlight(key)
	return false
}

// trackInFlight records that this pod has acquired the dedup slot for
// `key` and is about to enqueue (or itself process) the work. Pulled
// out of `dedupSeen` so the rollback path (`dedupForget`) and the
// release path (`ReleaseDedup`) can both un-track without the gating
// flow caring how the entry got there.
func (d *Dispatcher) trackInFlight(key string) {
	if key == "" {
		return
	}
	d.inFlightMu.Lock()
	defer d.inFlightMu.Unlock()
	if d.inFlight == nil {
		d.inFlight = map[string]struct{}{}
	}
	d.inFlight[key] = struct{}{}
}

// untrackInFlight removes an in-flight key. Called from both
// `dedupForget` (rollback when `Submit` failed) and `ReleaseDedup`
// (worker handler completed). Idempotent — calling twice on the same
// key is a no-op so the defensive double-release path stays safe.
func (d *Dispatcher) untrackInFlight(key string) {
	if key == "" {
		return
	}
	d.inFlightMu.Lock()
	defer d.inFlightMu.Unlock()
	delete(d.inFlight, key)
}

// dedupForget rolls back a record made by `dedupSeen` when the work
// the caller intended to gate (typically `submitter.Submit`) fails
// AFTER the dedup check. Without rollback, a webhook retry inside
// the dedup window would be silently dropped because the backend
// would still report the duplicate as seen. Tracked per Copilot
// review on PR #100 thread `PRRT_kwDOJKAEo85-5zE-`.
func (d *Dispatcher) dedupForget(ctx context.Context, key string) {
	if d.dedup == nil || key == "" {
		return
	}
	d.untrackInFlight(key)
	d.dedup.Forget(ctx, key)
}

// ReleaseDedup is the worker-side counterpart to the dispatcher's
// `dedupSeen` acquisition. The serve controller's pool handler must
// `defer d.ReleaseDedup(ctx, job.DedupKey)` so a successful (or
// failed) review releases the dedup record. Without the explicit
// release the K8s-Lease backend would persist the lease in etcd
// indefinitely (Kubernetes does NOT auto-delete `Lease` objects when
// `spec.leaseDurationSeconds` elapses — that field is metadata only),
// and a real follow-up push for the same PR would be silently dropped
// as a "duplicate". Safe to call with an empty key (no-op) so tests
// that build a `Job` without going through the handlers stay simple.
func (d *Dispatcher) ReleaseDedup(ctx context.Context, key string) {
	d.dedupForget(ctx, key)
}

// ReleaseAllInFlight releases every dedup record this pod is currently
// holding. The serve controller calls this AFTER `pool.Shutdown` returns
// so the K8s-Lease backend sees a clean drain even when the worker
// pool's drain budget cancelled jobs mid-flight: every in-flight key the
// dispatcher knows about gets a `Forget` so the lease is `Delete`d in
// etcd before this pod exits, and the next webhook delivery for the same
// PR re-acquires immediately on a fresh pod instead of waiting for the
// renewal loop to lapse and the takeover path to recover.
//
// Best-effort: per-key `Forget` failures log at warn but do not abort
// the loop — the renewal loop's lapse + takeover path is still the
// safety net for whatever this method could not release. Safe to call
// when no jobs are in flight (no-op).
func (d *Dispatcher) ReleaseAllInFlight(ctx context.Context) {
	d.inFlightMu.Lock()
	keys := make([]string, 0, len(d.inFlight))
	for key := range d.inFlight {
		keys = append(keys, key)
	}
	d.inFlight = map[string]struct{}{}
	d.inFlightMu.Unlock()

	if len(keys) == 0 {
		return
	}
	logger.Infof("dedup: releasing %d in-flight key(s) on shutdown so leases do not orphan", len(keys))
	for _, key := range keys {
		// Bypass `dedupForget` so the in-flight set (already drained
		// above under the lock) is not double-touched.
		if d.dedup != nil {
			d.dedup.Forget(ctx, key)
		}
	}
}

// RenewDedup runs the dedup-renewal loop for an in-flight job. The
// serve controller's worker handler kicks off `go d.RenewDedup(ctx,
// job.DedupKey)` for every job AFTER `dedupSeen` succeeded; the loop
// refreshes the dedup record's freshness window every
// `dedupRenewInterval` until ctx is cancelled. The cancellation comes
// from the worker handler's `defer cancelRenew()` on a child context
// it built specifically for the renewal loop — `ReleaseDedup` itself
// does NOT cancel any context, and the worker pool passes a long-lived
// base context to every job, so without that explicit child-context
// cancel the loop would outlive the job. The pairing lives in
// `serve_controller.go` (the worker handler builds `renewCtx`,
// defers `cancelRenew()`, then defers `ReleaseDedup` so the lease
// `Delete` runs after the renewal goroutine has stopped).
//
// Without this loop the K8s-Lease backend's `leaseDurationSeconds`
// would have to be set above the worst-case review wall-time — which
// in turn would block all webhook deliveries for the same PR for that
// duration whenever a pod crashed mid-review. Renewal lets the lease
// duration stay short (recovery in seconds, not minutes) without
// risking an actively-held lease being stolen by the takeover path.
//
// Safe to call with an empty key (returns immediately) so tests that
// build a `Job` without going through the handlers stay simple. Safe
// to call when the in-memory backend is wired (Renew is a no-op for
// it) so the worker handler does not have to branch on the backend
// type.
func (d *Dispatcher) RenewDedup(ctx context.Context, key string) {
	if d.dedup == nil || key == "" {
		return
	}
	ticker := time.NewTicker(dedupRenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.dedup.Renew(ctx, key)
		}
	}
}

// dedupRenewInterval is the cadence at which `RenewDedup` calls the
// backend's `Renew`. Half of `leaseDurationSeconds` (= `30s`) by
// design — the K8s-Lease backend documents the same 1:2 ratio as the
// invariant that prevents an in-flight lease from being stolen by
// the takeover path while the holder is still alive. Pulled out of
// the lease backend so unit tests for `RenewDedup` can stub the
// dedup interface without importing the lease package.
const dedupRenewInterval = 30 * time.Second

// SetDedup overrides the default in-memory dedup backend. The serve
// controller uses this to swap in `K8sLeaseDedup` when the bot is
// running inside a Kubernetes pod (detected via
// `KUBERNETES_SERVICE_HOST`), so cross-pod duplicate webhook
// deliveries are gated by the API server instead of a per-pod cache.
//
// **Concurrency contract:** must be called during initialisation,
// before the HTTP server starts handling webhook requests. The
// setter does not synchronise with `HandleAzureDevOps` /
// `HandleGitHub`, so swapping the backend at runtime would race with
// in-flight deliveries. The same contract applies to
// `SetADOHydrator`, `SetSubmitter`, and `SetGitHubTokenizer` — all
// four are wired once during DI bootstrap and never touched again.
func (d *Dispatcher) SetDedup(dedup WebhookDedup) {
	d.dedup = dedup
}

// SetADOHydrator overrides the default HTTP-based ADO PR hydrator. Tests
// substitute a stub that returns a canned `adoResource` without touching the
// network; production code does not need to call this.
//
// **Concurrency contract:** must be called during initialisation, before
// the HTTP server starts handling webhook requests. The setter does not
// synchronise with `HandleAzureDevOps`, so swapping the hydrator at
// runtime would race with in-flight deliveries. The same contract applies
// to `SetSubmitter` and `SetGitHubTokenizer` — all three are wired once
// during DI bootstrap and never touched again.
func (d *Dispatcher) SetADOHydrator(h ADOResourceHydrator) {
	d.adoHydrator = h
}

// SetADOIdentityResolver overrides the default HTTP-based resolver of
// the bot's own Azure DevOps identity. Tests substitute a hand-rolled
// resolver that answers from memory; production code does not need to
// call this.
//
// **Concurrency contract:** identical to `SetADOHydrator` — must be
// called during initialisation, before the HTTP server starts handling
// webhook requests.
func (d *Dispatcher) SetADOIdentityResolver(r ADOIdentityResolver) {
	d.adoIdentityResolver = r
}

// parseAllowedCIDRs validates and parses each CIDR entry once at startup so
// hot-path requests don't re-parse the same strings. Invalid entries are
// logged and skipped — this keeps the dispatcher boot-able if a single typo
// slips through, while still surfacing the problem in the operator log.
func parseAllowedCIDRs(raw []string) []netip.Prefix {
	if len(raw) == 0 {
		return nil
	}
	parsed := make([]netip.Prefix, 0, len(raw))
	for _, c := range raw {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(c)
		if err != nil {
			logger.Warnf("dispatcher: ignoring invalid CIDR %q in Server.AllowedSourceCIDRs: %v", c, err)
			continue
		}
		parsed = append(parsed, prefix)
	}
	return parsed
}

// SetSubmitter wires the worker pool used to enqueue review jobs. Called by the
// serve controller after the pool is constructed (the pool's handler closes over
// the dispatcher, so the cycle is broken at construction time).
func (d *Dispatcher) SetSubmitter(s Submitter) {
	d.submitter = s
}

// SetGitHubTokenizer wires the GitHub App installation token exchanger.
func (d *Dispatcher) SetGitHubTokenizer(t GitHubTokenizer) {
	d.githubTokenizer = t
}

// Settings exposes the loaded settings for handler-side allowlist checks.
func (d *Dispatcher) Settings() *entities.Settings {
	return d.settings
}

// HandlePR performs a review of a single PR. Called by worker goroutines.
// `userMentioned` signals the comment-event webhook path (a user comment
// containing `@code-guru` triggered this run); when true, the review-once
// gate in the command is bypassed so the user's explicit re-review request
// goes through. Push-triggered jobs pass false so the gate applies.
func (d *Dispatcher) HandlePR(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	pr forgeEntities.PullRequestDetail,
	ciPassed bool,
	userMentioned bool,
) error {
	aiReviewer := d.aiFactory.Create(d.settings)
	rulesRepo := d.rulesFactory.Create(d.settings)
	reviewCmd := commands.NewReviewCommand(aiReviewer, rulesRepo, d.detectorRegistry, d.metadataRepo)

	result, err := reviewCmd.Execute(ctx, provider, repo, pr, commands.ReviewOptions{
		CIPassed:                  ciPassed,
		SubmitNativeReview:        d.settings.AI.NativeReviewSubmissionEnabled(),
		MaxGuidelinesBytes:        d.settings.AI.GuidelinesBytes(),
		MaxPRDescriptionBytes:     d.settings.AI.PRDescriptionBytes(),
		ReviewDrafts:              d.settings.AI.ReviewDrafts,
		UserMentioned:             userMentioned,
		TrivialAutoMerge:          d.settings.Trivial.AutoMerge,
		TrivialMergeStrategy:      d.settings.Trivial.MergeStrategy,
		TrivialBypassPolicy:       d.settings.Trivial.BypassPolicy,
		TrivialAutoMergeAuthors:   d.settings.Trivial.AutoMergeAllowedAuthors,
		TrivialDeleteSourceBranch: d.settings.Trivial.DeleteSourceBranchEnabled(),
		BotIdentities:             d.settings.BotIdentities,
		LoadProjectGuidelines:     d.settings.AI.ProjectGuidelinesEnabled(),
		LoadPullRequestMetadata:   d.settings.AI.PullRequestMetadataEnabled(),
		BatchLargeReviews:         d.settings.AI.BatchLargeReviewsEnabled(),
		MaxReviewBatches:          d.settings.AI.ReviewBatches(),
	})
	if err != nil {
		return fmt.Errorf("review failed for PR #%d: %w", pr.ID, err)
	}

	logger.Infof("PR #%d review complete: verdict=%s, comments=%d", pr.ID, result.Verdict, len(result.Comments))

	if result.Verdict == "approve" && ciPassed {
		logger.Infof("PR #%d approved and CI passed -- auto-merge pending gitforge support", pr.ID)
	}

	return nil
}

// findToken returns the configured token for a given provider type. As a
// fallback (used by the env-only configuration where CODE_GURU_PROVIDER_TOKEN
// populates a single entry without a Type), a lone untyped provider entry is
// treated as the catch-all token for any provider.
func (d *Dispatcher) findToken(providerType string) string {
	for _, p := range d.settings.Providers {
		if p.Type == providerType {
			return p.Token
		}
	}
	if len(d.settings.Providers) == 1 && d.settings.Providers[0].Type == "" {
		return d.settings.Providers[0].Token
	}
	return ""
}

// allowedOrganization returns true when the org is on the allowlist (or the
// allowlist is empty, which means "allow all").
func (d *Dispatcher) allowedOrganization(org string) bool {
	allowed := d.settings.Server.AllowedOrganizations
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(allowed, org)
}

// allowedProject returns true when the project is on the allowlist (or the
// allowlist is empty). Empty project (e.g. GitHub) is always allowed.
func (d *Dispatcher) allowedProject(project string) bool {
	allowed := d.settings.Server.AllowedProjects
	if len(allowed) == 0 || project == "" {
		return true
	}
	return slices.Contains(allowed, project)
}

// writeError writes a status code and a short text body. Used for 4xx responses
// where the body content does not matter to the sender (webhook services ignore it).
func writeError(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	_, _ = fmt.Fprint(w, msg)
}
