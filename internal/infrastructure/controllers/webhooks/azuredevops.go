package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	logger "github.com/sirupsen/logrus"

	"github.com/rios0rios0/codeguru/internal/support"
)

// adoEvent is the minimal subset of an Azure DevOps Service Hook payload that
// HandleAzureDevOps needs. Unknown fields are ignored.
type adoEvent struct {
	EventType string      `json:"eventType"`
	Resource  adoResource `json:"resource"`
}

type adoResource struct {
	PullRequestID int           `json:"pullRequestId"`
	Status        string        `json:"status"`
	Title         string        `json:"title"`
	URL           string        `json:"url"`
	IsDraft       bool          `json:"isDraft"`
	SourceRefName string        `json:"sourceRefName"`
	TargetRefName string        `json:"targetRefName"`
	Repository    adoRepository `json:"repository"`
	CreatedBy     adoIdentity   `json:"createdBy"`
}

type adoRepository struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	RemoteURL string     `json:"remoteUrl"`
	Project   adoProject `json:"project"`
}

type adoProject struct {
	Name string `json:"name"`
}

// adoIdentity is the {displayName, uniqueName} shape Azure DevOps uses
// for every person reference in a webhook payload (PR author, comment
// author).
type adoIdentity struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

// adoIdentityName returns the most stable identity string for an ADO
// person reference: the uniqueName (UPN / email, e.g.
// `someone@example.com`) when present, else the displayName. This is
// what the webhook handlers store in `PullRequestDetail.Author` so the
// trivial auto-merge author allowlist (`Settings.Trivial.AutoMergeAllowedAuthors`)
// can match it — without it the ADO webhook path leaves Author empty and
// every PR fails the allowlist check, silently disabling auto-merge even
// for the allow-listed automation account.
func adoIdentityName(id adoIdentity) string {
	if id.UniqueName != "" {
		return id.UniqueName
	}
	return id.DisplayName
}

// HandleAzureDevOps processes Azure DevOps Service Hook events.
//
// Auth: Basic. ADO does not support HMAC signing on Service Hooks.
// Supported events: git.pullrequest.created and git.pullrequest.updated for
// active PRs. All other events return 204 No Content.
//
// The handler length is driven by the number of validation guard clauses it
// has to enforce in order before touching the worker queue. Splitting further
// would scatter the request flow across multiple methods without removing any
// branches.
//
//nolint:funlen // Single-shot HTTP handler whose length is proportional to its required validation flow.
func (d *Dispatcher) HandleAzureDevOps(w http.ResponseWriter, r *http.Request) {
	if !d.enforceSourceIPAllowlist(w, r, "ADO") {
		return
	}

	if authErr := VerifyBasicAuth(d.settings.Server.WebhookSecret, r.Header.Get("Authorization")); authErr != nil {
		logger.Warnf("ADO webhook rejected: %v", authErr)
		status := http.StatusUnauthorized
		if errors.Is(authErr, ErrMissingHeader) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "unauthorized")
		return
	}

	defer func() { _ = r.Body.Close() }()
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	var event adoEvent
	if jsonErr := json.Unmarshal(body, &event); jsonErr != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return
	}

	if event.EventType == adoCommentEventType {
		// Comment-event payload has a different resource shape (a
		// `comment` block alongside the `pullRequest` block instead of
		// the PR lifecycle's flat `resource`). Dispatched separately so
		// the mention detection + bypass-the-dedup-gate path is
		// readable end-to-end.
		d.handleADOComment(w, r, body)
		return
	}
	if !isSupportedADOEvent(event.EventType) {
		logger.Debugf("ADO webhook: ignoring event type %q", event.EventType)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if isClosedADOPullRequestStatus(event.Resource.Status) {
		logDebugf(
			"ADO webhook: PR #%d status %q is closed",
			event.Resource.PullRequestID,
			event.Resource.Status,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	token := d.findToken("azuredevops")
	if token == "" {
		logger.Errorf("ADO webhook: no azuredevops PAT configured")
		writeError(w, http.StatusInternalServerError, "no PAT configured")
		return
	}

	if !d.hydrateSkinnyADOResource(w, r, &event, token) {
		return
	}

	org := extractADOOrganization(event.Resource.Repository.RemoteURL)
	if !d.allowedOrganization(org) || !d.allowedProject(event.Resource.Repository.Project.Name) {
		// On allowlist rejection, dump the parsed shape AND a head of
		// the raw body at `Warn`. The diagnostic is intentionally NOT
		// gated behind `Debug` because the operational reality is that
		// rejection is rare (the org/project allowlist is small and
		// stable) but when it fires it always means something is
		// genuinely wrong with the wire payload — the management API
		// shows fully populated `resource.repository` blocks while the
		// pod sees them empty, and a `kubectl set env DEBUG=true` patch
		// keeps getting reverted by `terra apply` runs that race with
		// the diagnosis loop. Surfacing at `Warn` (the same level as
		// the existing `not on allowlist` line) makes the diagnostic
		// survive whatever the pod's log level happens to be set to.
		//
		// Body cap is `adoRawBodyLogLimit` (32 KB) to cover the typical
		// `git.pullrequest.*` envelope including the verbose
		// `message` / `detailedMessage` blocks plus the `resource`
		// block that sits after them — at 4 KB the cut landed right
		// before `resource`, which was the entire diagnostic of value.
		// 32 KB is still a constant per request and still eligible for
		// the `truncationSentinel` tail.
		logger.WithFields(deliveryFields(logger.Fields{
			"event_type":   event.EventType,
			"pull_id":      event.Resource.PullRequestID,
			"status":       event.Resource.Status,
			"repo_id":      event.Resource.Repository.ID,
			"repo_name":    event.Resource.Repository.Name,
			"remote_url":   event.Resource.Repository.RemoteURL,
			"project_name": event.Resource.Repository.Project.Name,
			"body_length":  len(body),
			"body_head":    support.TruncateBytesForLog(body, adoRawBodyLogLimit),
			"parsed_org":   org,
		})).Warnf("ADO webhook: org=%q project=%q not on allowlist", org, event.Resource.Repository.Project.Name)
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	provider, providerErr := d.providerRegistry.GetReviewProvider("azuredevops", token)
	if providerErr != nil {
		logger.Errorf("ADO webhook: failed to build provider: %v", providerErr)
		writeError(w, http.StatusInternalServerError, "provider error")
		return
	}

	repo := forgeEntities.Repository{
		ID:           event.Resource.Repository.ID,
		Name:         event.Resource.Repository.Name,
		Organization: org,
		Project:      event.Resource.Repository.Project.Name,
		RemoteURL:    event.Resource.Repository.RemoteURL,
	}
	pr := forgeEntities.PullRequestDetail{
		ID:           event.Resource.PullRequestID,
		Title:        event.Resource.Title,
		URL:          event.Resource.URL,
		Status:       event.Resource.Status,
		SourceBranch: refToBranch(event.Resource.SourceRefName),
		TargetBranch: refToBranch(event.Resource.TargetRefName),
		IsDraft:      event.Resource.IsDraft,
		// Author drives the trivial auto-merge allowlist; without it
		// every ADO webhook PR has an empty author and fails the gate.
		Author: adoIdentityName(event.Resource.CreatedBy),
	}

	dedupKey := fmt.Sprintf("ado:%s:%d", repo.ID, pr.ID)
	if d.dedupSeen(r.Context(), dedupKey) {
		logDebugf("ADO webhook: duplicate delivery for PR #%d in %s/%s — skipping", pr.ID, repo.Project, repo.Name)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "duplicate")
		return
	}

	if submitErr := d.submitter.Submit(
		Job{Provider: provider, Repo: repo, PR: pr, CIPassed: false, DedupKey: dedupKey},
	); submitErr != nil {
		logger.Errorf("ADO webhook: submit failed: %v", submitErr)
		// Roll back the dedup record so a webhook retry inside the
		// dedup window is not silently dropped — the backend must
		// only retain keys that actually made it onto the worker
		// queue.
		d.dedupForget(r.Context(), dedupKey)
		writeError(w, http.StatusServiceUnavailable, "queue full")
		return
	}

	logInfof("ADO webhook: enqueued PR #%d in %s/%s", pr.ID, repo.Project, repo.Name)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "accepted")
}

// supportedADOEvents lists the event types HandleAzureDevOps acts on. Defined as a
// map (Mapper pattern) to make extension trivial.
//
//nolint:gochecknoglobals // read-only lookup table used as a constant
var supportedADOEvents = map[string]struct{}{
	"git.pullrequest.created": {},
	"git.pullrequest.updated": {},
}

func isSupportedADOEvent(eventType string) bool {
	_, ok := supportedADOEvents[eventType]
	return ok
}

// adoCommentEventType is the Azure DevOps event type fired when a user
// posts (or edits / deletes) a comment on a PR. Carried as a constant
// instead of joining `supportedADOEvents` because the resource shape
// differs from the PR-lifecycle events and the handler dispatches it
// down a separate code path (mention detection + bypass-the-dedup-gate).
const adoCommentEventType = "ms.vss-code.git-pullrequest-comment-event"

// closedADOPullRequestStatuses lists the values of `resource.status` that
// represent a Pull Request the bot must NOT review. The check is
// allow-list-by-rejection: anything not in this set (including the empty
// string and any future enum value Azure DevOps may add) proceeds. ADO's
// `git.pullrequest.created` / `git.pullrequest.updated` events are
// observed in the wild to ship `resource.status: ""` on certain
// commit-only updates — particularly on internal-terraform PR #NNNN captured
// in the diagnosis log — and an empty status used to short-circuit the
// handler with a 204, dropping every push silently. The new shape only
// rejects PRs that are explicitly closed.
//
//nolint:gochecknoglobals // read-only lookup table used as a constant
var closedADOPullRequestStatuses = map[string]struct{}{
	"abandoned": {},
	"completed": {},
}

func isClosedADOPullRequestStatus(status string) bool {
	_, ok := closedADOPullRequestStatuses[strings.ToLower(strings.TrimSpace(status))]
	return ok
}

// extractADOOrganization parses the org slug out of an ADO remote URL of the form
// https://dev.azure.com/{org}/{project}/_git/{repo} or
// https://{org}.visualstudio.com/{project}/_git/{repo}.
func extractADOOrganization(remoteURL string) string {
	if remoteURL == "" {
		return ""
	}
	u, err := url.Parse(remoteURL)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "dev.azure.com" {
		segments := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if len(segments) > 0 {
			return segments[0]
		}
		return ""
	}
	if before, ok := strings.CutSuffix(host, ".visualstudio.com"); ok {
		return before
	}
	return ""
}

// refToBranch trims the refs/heads/ prefix from a Git ref name.
func refToBranch(ref string) string {
	return strings.TrimPrefix(ref, "refs/heads/")
}

// hydrateSkinnyADOResource replaces a stripped-down org-wide-subscription
// `resource` block with the full envelope fetched from the ADO REST API.
// Returns true when the caller should keep going, false when a response
// has already been written and processing must stop.
//
// The token is supplied by the caller (`HandleAzureDevOps`) so the lookup
// runs once per request — avoiding a duplicated `findToken("azuredevops")`
// in both the hydration branch and the provider-construction branch
// further down.
//
// The function is split out of HandleAzureDevOps to keep that handler's
// cognitive complexity within the linter's 20-branch budget — the
// hydration step is a self-contained pre-filter and tests cover it
// directly via the hydrator unit tests.
func (d *Dispatcher) hydrateSkinnyADOResource(
	w http.ResponseWriter,
	r *http.Request,
	event *adoEvent,
	token string,
) bool {
	if !isSkinnyADOResource(event.Resource) {
		logDebugf(
			"ADO webhook: PR #%d arrived with full resource block (project-scoped subscription) — skipping hydration",
			event.Resource.PullRequestID,
		)
		return true
	}

	logDebugf(
		"ADO webhook: PR #%d arrived with skinny resource block (org-wide subscription) — hydrating via REST API",
		event.Resource.PullRequestID,
	)

	if d.adoHydrator == nil {
		logger.Errorf("ADO webhook: skinny payload but no hydrator wired")
		writeError(w, http.StatusInternalServerError, "hydrator not wired")
		return false
	}

	hydrated, err := d.adoHydrator.Hydrate(r.Context(), event.Resource.URL, token)
	if err != nil {
		logWarnf(
			"ADO webhook: hydrate PR #%d via %q failed: %v",
			event.Resource.PullRequestID,
			event.Resource.URL,
			err,
		)
		writeError(w, http.StatusBadGateway, "hydration failed")
		return false
	}
	event.Resource = mergeHydratedADOResource(event.Resource, hydrated)

	// Re-apply the closed-status guard against the hydrated value: a
	// `git.pullrequest.updated` for an `abandoned` PR carries an empty
	// `status` in the skinny shape, so the earlier check let it through.
	if isClosedADOPullRequestStatus(event.Resource.Status) {
		logDebugf(
			"ADO webhook: PR #%d hydrated to status %q (closed)",
			event.Resource.PullRequestID,
			event.Resource.Status,
		)
		w.WriteHeader(http.StatusNoContent)
		return false
	}
	return true
}

// adoRawBodyLogLimit caps the number of body bytes echoed at `Warn` on the
// allowlist-rejection diagnostic. 32 KB covers the canonical ADO payload
// envelope including the `message` / `detailedMessage` blocks AND the
// `resource` block that sits after them — at the previous 4 KB cap the
// cut landed right before `resource`, which was the entire diagnostic of
// value. The `Warn`-level emission is intentionally not gated behind
// `Debug` because the rejection itself is already a rare, operator-level
// signal and the body cap keeps the per-request cost bounded.
const adoRawBodyLogLimit = 32768

// adoCommentEvent is the minimal subset of the ADO comment-event
// payload the mention handler needs. Different shape from `adoEvent`:
// the resource carries a `comment` block (the new comment) alongside a
// `pullRequest` block (the PR the comment was posted on), instead of
// the PR lifecycle's flat `resource`.
type adoCommentEvent struct {
	EventType string `json:"eventType"`
	Resource  struct {
		Comment struct {
			Content string      `json:"content"`
			Author  adoIdentity `json:"author"`
		} `json:"comment"`
		PullRequest struct {
			PullRequestID int           `json:"pullRequestId"`
			Title         string        `json:"title"`
			URL           string        `json:"url"`
			Repository    adoRepository `json:"repository"`
			CreatedBy     adoIdentity   `json:"createdBy"`
		} `json:"pullRequest"`
	} `json:"resource"`
}

// handleADOComment processes the ADO comment event so a user can
// request a re-review by mentioning the bot in a PR comment — as
// `@code-guru`, as the account the deployment posts under (any
// `bot_identities` entry, see `support.HasMention`), or by picking the
// bot out of the ADO comment box's @-autocomplete (see
// `adoCommentMentionsBot`). The handler:
//
//   - returns 400 on malformed JSON;
//   - returns 204 when the comment body mentions none of them
//     (preserves the operator's signal-to-noise ratio in pod logs);
//   - returns 403 when the org / project is off-allowlist;
//   - enqueues the matched comment as a job with `UserMentioned=true`
//     so the review-once gate is bypassed on the worker side.
//
// The dedup gate is intentionally NOT applied to mention deliveries:
// a user posting `@code-guru` is an explicit re-review request and
// should always go through.
func (d *Dispatcher) handleADOComment(w http.ResponseWriter, r *http.Request, body []byte) {
	var event adoCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return
	}
	if !d.adoCommentMentionsBot(r.Context(), &event) {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// A mention in a comment the bot itself authored (e.g. the
	// "mention `@code-guru` ... to try again" line in its own "review
	// failed" annotation) must NOT trigger a re-review. ADO fires a
	// comment webhook for the bot's own posts too, so acting on them
	// spins an infinite review->fail->annotate->webhook loop that floods
	// the PR — observed on an oversized diff that could never pass review.
	if commenter := adoIdentityName(event.Resource.Comment.Author); support.IsBotAuthor(
		d.settings.BotIdentities...)(
		commenter,
	) {
		logDebugf(
			"ADO webhook: comment on PR #%d is authored by the bot itself (%s); skipping self-triggered re-review",
			event.Resource.PullRequest.PullRequestID, commenter,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	pr := event.Resource.PullRequest
	org := extractADOOrganization(pr.Repository.RemoteURL)
	if !d.allowedOrganization(org) || !d.allowedProject(pr.Repository.Project.Name) {
		logger.Warnf("ADO webhook: org=%q project=%q not on allowlist (mention path)",
			org, pr.Repository.Project.Name)
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	token := d.findToken("azuredevops")
	if token == "" {
		logger.Errorf("ADO webhook: no azuredevops PAT configured (mention path)")
		writeError(w, http.StatusInternalServerError, "no PAT configured")
		return
	}

	provider, providerErr := d.providerRegistry.GetReviewProvider("azuredevops", token)
	if providerErr != nil {
		logger.Errorf("ADO webhook: failed to build provider (mention path): %v", providerErr)
		writeError(w, http.StatusInternalServerError, "provider error")
		return
	}

	repo := forgeEntities.Repository{
		ID:           pr.Repository.ID,
		Name:         pr.Repository.Name,
		Organization: org,
		Project:      pr.Repository.Project.Name,
		RemoteURL:    pr.Repository.RemoteURL,
	}
	job := Job{
		Provider: provider,
		Repo:     repo,
		PR: forgeEntities.PullRequestDetail{
			PullRequest: forgeEntities.PullRequest{
				ID:    pr.PullRequestID,
				Title: pr.Title,
				URL:   pr.URL,
			},
			// Carry the PR author so a mention re-review that ends in a
			// trivial-approve still honours the auto-merge allowlist.
			Author: adoIdentityName(pr.CreatedBy),
		},
		UserMentioned: true,
	}

	if submitErr := d.submitter.Submit(job); submitErr != nil {
		logger.Errorf("ADO webhook: submit failed (mention path): %v", submitErr)
		writeError(w, http.StatusServiceUnavailable, "queue full")
		return
	}
	commenter := adoIdentityName(event.Resource.Comment.Author)
	logInfof("ADO webhook: enqueued mention re-review for PR #%d in %s/%s (commenter=%s)",
		pr.PullRequestID, repo.Project, repo.Name, commenter)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "accepted")
}

// adoCommentMentionsBot reports whether an Azure DevOps PR comment asks
// this bot for a re-review. Three forms count:
//
//  1. the literal `@code-guru` token;
//  2. any mention derived from `bot_identities` — the account the
//     deployment posts under;
//  3. an @-autocompleted mention of the bot itself. The ADO comment box
//     replaces the account a user picked with `@<identity-guid>`
//     markup, so the name they selected never reaches the webhook and
//     forms (1) and (2) cannot match it. The GUID is compared against
//     the identity the bot's own PAT authenticates as, which makes the
//     autocompleted mention work with no configuration at all.
//
// Forms (1) and (2) are one plain string scan (`support.HasMention`) —
// the only branch the overwhelming majority of comments ever reach. The
// identity lookup behind (3) runs only when the body carries a
// well-formed `@<guid>` that scan already failed on, so an ordinary
// comment costs no REST round-trip and no log line.
//
// Exactly one outcome logs at Info: a `@<guid>` mention arrived on an
// allow-listed organisation and the bot could NOT determine its own
// identity — meaning a mention of the bot itself would be dropped right
// there, which is the silent failure this path exists to end. Every
// other miss is Debug: a GUID that resolves to somebody else is one
// human @-mentioning another (the common use of this markup, not a
// failure), and an off-allowlist delivery never attempted a lookup at
// all. Pinned per review on PR #329.
func (d *Dispatcher) adoCommentMentionsBot(ctx context.Context, event *adoCommentEvent) bool {
	content := event.Resource.Comment.Content
	prID := event.Resource.PullRequest.PullRequestID

	if support.HasMention(content, d.settings.BotIdentities...) {
		return true
	}

	mentionedIDs := support.ExtractMentionedIdentityIDs(content)
	if len(mentionedIDs) == 0 {
		logDebugf(
			"ADO webhook: comment on PR #%d mentions neither @code-guru nor a configured bot identity; skipping",
			prID,
		)
		return false
	}

	org := extractADOOrganization(event.Resource.PullRequest.Repository.RemoteURL)
	selfID := d.resolveADOSelfIdentity(ctx, org)
	for _, id := range mentionedIDs {
		if support.EqualIdentityID(selfID, id) {
			logInfof(
				"ADO webhook: comment on PR #%d @-mentions this bot's own Azure DevOps identity (%q); treating as a re-review request",
				prID,
				selfID,
			)
			return true
		}
	}

	switch {
	case selfID != "":
		// We compared and these ids belong to somebody else. One human
		// @-mentioning another is the COMMON use of this markup, not a
		// failure, so it stays out of the operator's default log view.
		logDebugf(
			"ADO webhook: comment on PR #%d @-mentions %v, none of which is this bot (%q); skipping",
			prID,
			mentionedIDs,
			selfID,
		)
	case d.allowedOrganization(org):
		// We should have been able to name ourselves and could not, so an
		// autocompleted mention OF THIS BOT would be dropped right here
		// with nothing to show for it. That is the silent failure this
		// path exists to end, and it is the only branch that earns Info.
		logInfof(
			"ADO webhook: comment on PR #%d carries @-autocompleted mention(s) %v but this bot's own Azure DevOps "+
				"identity could not be resolved, so a mention of it cannot be recognised — check the azuredevops PAT "+
				"and the preceding log line, or list the account in `bot_identities`; skipping",
			prID,
			mentionedIDs,
		)
	default:
		// Off-allowlist: no lookup was attempted (see resolveADOSelfIdentity),
		// so there is nothing here an operator asked to hear about.
		logDebugf(
			"ADO webhook: comment on PR #%d is for off-allowlist org %q; no identity lookup was attempted; skipping",
			prID,
			org,
		)
	}
	return false
}

// resolveADOSelfIdentity returns the identity id this bot's PAT
// authenticates as in `organization`, or "" when it cannot be
// determined. Never fails the delivery: an unresolvable identity just
// means the autocompleted-mention form does not match, exactly as
// before this path existed.
//
// The organisation allowlist is enforced BEFORE the lookup even though
// the caller checks it again later. The slug comes from the webhook
// payload, and this is the one place on the mention path that spends
// the bot's PAT on an outbound request — so an org the operator never
// allow-listed must not be able to make the bot authenticate anywhere
// on its behalf, however constrained the resulting URL is.
func (d *Dispatcher) resolveADOSelfIdentity(ctx context.Context, organization string) string {
	if d.adoIdentityResolver == nil || organization == "" {
		return ""
	}
	if !d.allowedOrganization(organization) {
		return ""
	}
	token := d.findToken("azuredevops")
	if token == "" {
		logger.Debugf("ADO webhook: no azuredevops PAT configured; cannot resolve this bot's own identity")
		return ""
	}

	selfID, err := d.adoIdentityResolver.ResolveSelfID(ctx, organization, token)
	if err != nil {
		logWarnf(
			"ADO webhook: could not resolve this bot's own Azure DevOps identity in org %q "+
				"(@-autocompleted mentions of it will not be recognised): %v",
			organization, err,
		)
		return ""
	}
	return selfID
}
