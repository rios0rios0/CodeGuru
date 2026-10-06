package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/domain/repositories"
	"github.com/rios0rios0/codeguru/internal/support"
)

// Verdict strings emitted on the review path. Mirrors the unexported
// constants in `internal/support/verdict_mapper.go`; defined locally
// because those are private to that package.
//
// `request_changes` belongs to the LLM vocabulary and `reject` to the
// trivial detectors'; both are listed so `batchVerdictRank` can order the
// full verdict surface when merging a batched review.
const (
	verdictApprove        = "approve"
	verdictComment        = "comment"
	verdictRequestChanges = "request_changes"
	verdictReject         = "reject"
)

// Review is the interface for the review command.
type Review interface {
	Execute(
		ctx context.Context,
		provider forgeEntities.ReviewProvider,
		repo forgeEntities.Repository,
		pr forgeEntities.PullRequestDetail,
		opts ReviewOptions,
	) (*entities.ReviewResult, error)
}

// ReviewOptions holds runtime options for a single review.
type ReviewOptions struct {
	DryRun   bool
	Verbose  bool
	CIPassed bool

	// MaxGuidelinesBytes bounds the reviewed repository's own CLAUDE.md
	// before it reaches the prompt. Zero (the "unset" state, used by
	// hand-built commands and tests) falls back to
	// entities.AIConfig.GuidelinesBytes()'s default, so a caller that
	// never wires the budget still gets the shipped one. Wired from
	// settings.AI.GuidelinesBytes() at each call site.
	MaxGuidelinesBytes int

	// MaxPRDescriptionBytes bounds the pull request description before it
	// reaches the prompt. Zero falls back to the shipped default, on the
	// same argument as MaxGuidelinesBytes. Wired from
	// settings.AI.PRDescriptionBytes() at each call site.
	MaxPRDescriptionBytes int

	// SubmitNativeReview, when true, also records a native PR review on the
	// underlying provider (Approved / Changes Requested) so the verdict
	// surfaces in the platform's reviewer panel rather than only in the
	// completion annotation. Best-effort: a failure to submit logs at warn
	// and the existing text annotation still posts. Wired from
	// settings.AI.NativeReviewSubmissionEnabled() at each call site, which
	// resolves the tri-state YAML / env config (default true).
	SubmitNativeReview bool

	// ReviewDrafts, when false, causes Execute to short-circuit on draft
	// PRs (PullRequestDetail.IsDraft) before fetching files or calling the
	// AI. When true, drafts go through the full review path. Wired from
	// settings.AI.ReviewDrafts at each call site.
	ReviewDrafts bool

	// UserMentioned signals that this review run was triggered by a
	// user comment that contained `@code-guru` (handled by the comment-
	// event webhook). When true, the review-once gate is bypassed even
	// if the bot has already posted a "review complete" annotation —
	// the user has explicitly asked for another pass. Push-triggered
	// reviews (the default) leave this false so the gate applies.
	UserMentioned bool

	// TrivialAutoMerge, when true, calls `provider.MergePullRequest`
	// after a trivial-approve verdict. Off by default — the gate is
	// "operator explicitly opted-in" because auto-merge is a
	// destructive cross-system action that bypasses human review.
	// Wired from `Settings.Trivial.AutoMerge` at each call site.
	// Failures degrade gracefully: a merge error logs at warn and the
	// verdict still stands.
	TrivialAutoMerge bool

	// TrivialMergeStrategy is the gitforge merge-strategy string
	// (`"merge"` / `"squash"` / `"rebase"`) used when
	// `TrivialAutoMerge` is true. Empty falls back to the platform's
	// default merge strategy. Wired from
	// `Settings.Trivial.MergeStrategy`.
	TrivialMergeStrategy string

	// TrivialBypassPolicy, when true, asks the provider to skip
	// branch policies (`Required reviewers`, `Minimum approver count`,
	// etc.) on the auto-merge call. Off by default — bypass strictly
	// requires the bot's identity to hold the platform-level
	// `Bypass policies when completing pull requests` permission, so
	// turning this on without that permission turns previously-working
	// auto-merges into hard 403s. Wired from
	// `Settings.Trivial.BypassPolicy`.
	TrivialBypassPolicy bool

	// TrivialAutoMergeAuthors restricts auto-merge to PRs opened by one
	// of the listed account identities (e.g. the `autobump` / `autoupdate`
	// / config-automation service account). Triviality decides whether a
	// PR is ELIGIBLE to auto-merge; this allowlist decides whether its
	// AUTHOR is trusted to merge unattended. When non-empty, a trivial-
	// approve verdict only auto-merges if the PR author matches an entry
	// (exact, case-insensitive). When empty, auto-merge falls back to the
	// historical "any author" behaviour for backward compatibility — not
	// recommended with `TrivialBypassPolicy`, since it force-merges every
	// trivial PR (including a human's docs PR) past `Required reviewers`.
	// Wired from `Settings.Trivial.AutoMergeAllowedAuthors`.
	TrivialAutoMergeAuthors []string

	// TrivialDeleteSourceBranch, when true, asks the provider to delete the
	// PR's source branch after a successful trivial auto-merge (gitforge's
	// `WithDeleteSourceBranch`). Only has effect when `TrivialAutoMerge` also
	// fires — without a merge there is no branch to delete. Defaults ON: wired
	// from `Settings.Trivial.DeleteSourceBranchEnabled()` (nil YAML/env → true)
	// at each call site. Branch deletion is best-effort in the provider: a
	// failure is logged there and never fails the merge.
	TrivialDeleteSourceBranch bool

	// BotIdentities lists the account identities code-guru posts under
	// (e.g. a service-account login / email on a self-hosted Azure
	// DevOps deployment). Used on the re-review path so the conversation
	// walk can recognise the bot's OWN prior comments and the LLM can
	// resolve them instead of re-posting the same findings. The built-in
	// `code-guru[bot]` / `code-guru@...` shapes are always recognised;
	// these are ADDITIONAL identities for deployments that post under a
	// different account. Optional — when empty, the re-review path still
	// self-detects the bot from the author of its own PR-wide status
	// annotations on the PR (see `support.DetectBotAuthors`). Wired from
	// `Settings.BotIdentities` at each call site.
	BotIdentities []string

	// LoadProjectGuidelines, when true, fetches the reviewed
	// repository's own root `CLAUDE.md` via the provider's file-access
	// API and forwards it to the LLM as project-specific review context
	// (`ReviewRequest.ProjectGuidelines`), so the review honours the
	// project's own conventions on any provider. The fetch is skipped
	// when the PR itself modifies CLAUDE.md — the diff already shows it
	// — and is best-effort otherwise: a missing file or provider error
	// logs and the review proceeds without guidelines. Wired from
	// `settings.AI.ProjectGuidelinesEnabled()` at each call site, which
	// resolves the tri-state YAML / env config (default true).
	LoadProjectGuidelines bool

	// BatchLargeReviews, when true, makes a pull request whose prompt
	// exceeds the AI model's context window fall back to a BATCHED
	// review — the files are split into chunks that fit, reviewed one
	// after another, and merged into a single review — instead of
	// posting the "this PR is too large, no review was posted" notice
	// and giving up. Defaults ON: wired from
	// `settings.AI.BatchLargeReviewsEnabled()` (nil YAML/env → true) at
	// each call site. Operators opt OUT with
	// `ai.batch_large_reviews: false` when the extra model calls (one
	// per batch, on a paid backend) are not worth it.
	BatchLargeReviews bool

	// MaxReviewBatches caps how many batches a single batched review may
	// consume, bounding the cost and latency of one oversized pull
	// request. Zero (the "unset" state, used by hand-built commands and
	// tests) falls back to `entities.AIConfig.ReviewBatches()`'s default,
	// on the same argument as MaxGuidelinesBytes. Files left over when
	// the cap is reached are reported as unreviewed rather than silently
	// dropped. Wired from `settings.AI.ReviewBatches()` at each call site.
	MaxReviewBatches int

	// LoadPullRequestMetadata, when true, fetches the PR's author-
	// supplied metadata — its description and commit count — and
	// forwards it to the LLM as intent context
	// (`ReviewRequest.Metadata`), so the model can judge whether the
	// diff actually does what the title, branch name, and description
	// claim. Best-effort: an unsupported provider or a fetch error logs
	// at debug and the review proceeds without the context. Wired from
	// `settings.AI.PullRequestMetadataEnabled()` at each call site,
	// which resolves the tri-state YAML / env config (default true).
	LoadPullRequestMetadata bool
}

// ReviewCommand orchestrates a single PR review.
type ReviewCommand struct {
	aiReviewer       repositories.AIReviewerRepository
	rulesRepo        repositories.RulesRepository
	detectorRegistry repositories.TrivialDetectorRegistry
	metadataRepo     repositories.PullRequestMetadataRepository
}

// NewReviewCommand creates a new ReviewCommand. `metadataRepo` may be
// nil (mirroring the nil `detectorRegistry` on the review-all path):
// the PR-metadata fetch is then skipped and the review runs exactly as
// it did before the metadata context existed.
func NewReviewCommand(
	aiReviewer repositories.AIReviewerRepository,
	rulesRepo repositories.RulesRepository,
	detectorRegistry repositories.TrivialDetectorRegistry,
	metadataRepo repositories.PullRequestMetadataRepository,
) *ReviewCommand {
	return &ReviewCommand{
		aiReviewer:       aiReviewer,
		rulesRepo:        rulesRepo,
		detectorRegistry: detectorRegistry,
		metadataRepo:     metadataRepo,
	}
}

// Execute performs a review of a single pull request.
func (c *ReviewCommand) Execute(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	pr forgeEntities.PullRequestDetail,
	opts ReviewOptions,
) (*entities.ReviewResult, error) {
	logger.Infof("reviewing PR #%d: %s", pr.ID, pr.Title)

	// Drafts are skipped by default — most teams treat draft PRs as
	// work-in-progress and do not want to spend AI budget on something
	// the author has explicitly marked unfinished. Operators that want
	// to opt back in flip `settings.AI.ReviewDrafts` (env override
	// `CODE_GURU_AI_REVIEW_DRAFTS=true`). The skip happens BEFORE the
	// "reviewing" marker so a draft PR doesn't accumulate a marker
	// thread that will never get a completion annotation. The verdict
	// is intentionally `comment` (not `approve` / `reject`) so any
	// downstream native-review submission also short-circuits.
	if skip := c.shouldSkip(ctx, provider, repo, pr, opts); skip != nil {
		return skip, nil
	}

	// fetch changed files
	files, err := provider.GetPullRequestFiles(ctx, repo, pr.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to get PR files: %w", err)
	}

	if len(files) == 0 {
		logger.Infof("PR #%d has no changed files, skipping", pr.ID)
		return &entities.ReviewResult{PullRequestURL: pr.URL, Verdict: verdictComment, Summary: "no files changed"}, nil
	}

	// collect file paths. Normalised via `normalizeFilePath` to strip
	// the leading `/` Azure DevOps prefixes onto every path — without
	// it the bump detectors compare `/CHANGELOG.md` against their
	// `CHANGELOG.md` required-files set and incorrectly flag an
	// otherwise-valid bump PR as missing the changelog.
	var paths []string
	for _, f := range files {
		paths = append(paths, normalizeFilePath(f.Path))
	}

	// Trivial PR detection runs unconditionally — each detector
	// self-validates what counts as "trivial enough" (bump detectors
	// require a matching `.autobump.yaml`, docs-only requires every
	// file be Markdown). No CI gate: every entry point hardcodes
	// CIPassed=false, so any gate on it would silently disable the
	// entire trivial path.
	if c.detectorRegistry != nil {
		if result := c.handleTrivialDetection(ctx, provider, repo, pr, paths, opts); result != nil {
			return result, nil
		}
	}

	// Post a "reviewing" marker so the PR author sees the bot has the
	// work and is on it — without this, on a complex diff the bot can
	// take 5-10 minutes (or fail silently mid-review, see PR #98) and
	// the author would have no signal to wait. The marker is
	// intentionally placed AFTER the trivial-detection gate so trivial
	// PRs (auto-approve / auto-reject — already-instant signal) don't
	// get the noise of an extra "reviewing" thread on top of the
	// verdict. Skipped under DryRun so `terra plan`-style invocations
	// stay silent.
	if !opts.DryRun {
		c.postReviewingMarker(ctx, provider, repo, pr.ID)
	}

	// build diffs and run AI review
	diffs, err := c.buildDiffs(ctx, provider, repo, pr.ID, files)
	if err != nil {
		return nil, err
	}

	// load rules for detected languages
	languages := support.ClassifyFiles(paths)
	rules, err := c.rulesRepo.LoadForLanguages(languages, paths)
	if err != nil {
		return nil, fmt.Errorf("failed to load rules: %w", err)
	}

	logger.Infof("loaded %d rules for languages: %v", len(rules), languages)

	// build review request and call AI. On the mention re-review path
	// we additionally walk the bot's prior inline review threads (and
	// every reply on each one) so the LLM can read the dialogue before
	// deciding whether to repeat / withdraw / respond to its earlier
	// findings — see `support.BuildReviewConversation` for the shape.
	// First-pass reviews leave Conversation nil so the prompt is
	// byte-for-byte the same as before this change landed.
	conversation := c.buildConversation(ctx, provider, repo, pr.ID, opts)
	request := entities.ReviewRequest{
		Repository:   repo,
		PullRequest:  pr,
		Diffs:        diffs,
		Rules:        rules,
		Conversation: conversation,
		// Best-effort project context: the reviewed repository's own
		// CLAUDE.md, so the LLM judges the diff against the project's
		// conventions. Empty (and the prompt unchanged) when disabled,
		// unavailable, or when the PR itself modifies the file.
		ProjectGuidelines: c.loadProjectGuidelines(ctx, provider, repo, pr.ID, paths, opts),
		// Best-effort intent context: the PR's description and commit
		// count, so the LLM judges whether the diff does what the
		// author claims. Zero (and the prompt unchanged) when disabled
		// or unavailable.
		Metadata: c.loadPullRequestMetadata(ctx, provider, repo, pr.ID, opts),
	}

	result, err := c.aiReviewer.ReviewDiff(ctx, request)
	// A pull request bigger than the model's context window is not a dead
	// end — it is simply more than one prompt can hold. Fall back to
	// reviewing it in batches (announced on the PR, because the run takes
	// several times as long) instead of abandoning exactly the pull
	// requests that most need a reviewer. Only a run where NOT ONE batch
	// succeeded falls through to the failure annotation below.
	if errors.Is(err, support.ErrContextWindowExceeded) && opts.BatchLargeReviews {
		result, err = c.reviewLargePullRequest(
			ctx, provider, repo, pr.ID, request, err, reviewFailureContextFrom(files, diffs), opts,
		)
	}
	if err != nil {
		// Post a user-visible failure annotation so the PR author
		// understands the silence after the "reviewing" marker is a
		// crash, not the bot still working. Without this notice, the
		// marker promises a review that never arrives — captured live
		// across PRs `#NNNN` / `#NNNN` / `#NNNN` / `#NNNN` /
		// `#NNNN` / `#NNNN` on `2026-05-01` where ~half of all
		// reviews failed silently with `claude CLI failed: exit
		// status 1`. With dedup (PR #100) only one pod attempts the
		// review per PR, so a single failure means total silence
		// unless we annotate. Best-effort: a failure to post the
		// annotation logs at `Warn` and the original error still
		// bubbles up to the worker. Skipped when the PR has been
		// closed mid-flight — same gate as the success path below.
		if !opts.DryRun && !isPullRequestClosed(ctx, provider, repo, pr.ID) {
			c.postReviewFailedAnnotation(ctx, provider, repo, pr.ID, err, reviewFailureContextFrom(files, diffs))
		}
		return nil, fmt.Errorf("AI review failed: %w", err)
	}

	result.PullRequestURL = pr.URL

	if !opts.DryRun {
		// One PR-status check gates BOTH `postComments` AND the
		// completion annotation — without the unified gate the bot
		// would still post the "review complete" notice on a merged
		// PR, undermining the skip (Copilot review on PR #105
		// thread `PRRT_kwDOJKAEo85-6f_Y`). Doing the check once at
		// the call site (instead of inside `postComments`) also
		// halves the API calls on the success path.
		if isPullRequestClosed(ctx, provider, repo, pr.ID) {
			logger.Warnf(
				"PR #%d: closed mid-flight, skipping review post (verdict=%s, comments=%d would have been posted)",
				pr.ID,
				result.Verdict,
				len(result.Comments),
			)
		} else {
			// On the mention re-review path, act on the LLM's per-thread
			// resolution decisions BEFORE posting any new inline comments.
			// `applyThreadResolutions` posts one short reply per prior
			// thread + auto-resolves the threads marked `resolved`, and
			// returns the set of (file, line) pairs the LLM has already
			// addressed so `postComments` can drop new comments that
			// would otherwise land as a duplicate on the same anchor.
			handled := c.applyThreadResolutions(ctx, provider, repo, pr.ID, conversation, result.ThreadResolutions)
			c.postComments(ctx, provider, repo, pr.ID, result, handled)
			// After the inline / summary comments land, post a tiny
			// completion notice so the PR author sees a visible
			// transition from "reviewing" (PR #102) to "done".
			// Without this, the marker stays open-ended and the
			// author has to count comments to infer the bot
			// finished. The notice is intentionally a separate
			// thread (not an edit of the marker) because gitforge
			// does not yet expose a thread-update method — see
			// task `#47`. Once that lands, this can be replaced
			// with marker-thread auto-close.
			c.postReviewCompleteAnnotation(ctx, provider, repo, pr.ID, result)
			// Native review submission records the reviewer-panel vote
			// (Approved / Changes Requested / Waiting for Author).
			// Body is intentionally empty so the submission does NOT
			// duplicate the annotation's summary as a second PR-wide
			// comment on Azure DevOps. Mirrors the trivial fast path
			// which already passes "" — both paths now render the
			// rationale exactly once, inside the completion annotation
			// produced by `buildReviewCompleteBody` (which surfaces
			// `result.Summary` since PR #124).
			c.submitNativeReview(ctx, provider, repo, pr.ID, result.Verdict, "", opts)
		}
	}

	return result, nil
}

func (c *ReviewCommand) handleTrivialDetection(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	pr forgeEntities.PullRequestDetail,
	paths []string,
	opts ReviewOptions,
) *entities.ReviewResult {
	// build detection context with file content fetcher if available
	dctx := repositories.DetectionContext{
		Files:    paths,
		RepoName: repo.Name,
	}
	if fap, ok := provider.(forgeEntities.FileAccessProvider); ok {
		dctx.FileContentFetcher = &forgeFileContentFetcherAdapter{provider: fap, repo: repo}
	}

	detector, detection, found := c.detectorRegistry.Detect(ctx, dctx)
	if !found {
		return nil
	}

	logger.Infof(
		"PR #%d detected as trivial by %q adapter (verdict=%s), skipping LLM review",
		pr.ID, detector.Name(), detection.Verdict,
	)

	result := &entities.ReviewResult{
		PullRequestURL: pr.URL,
		Verdict:        detection.Verdict,
		Summary:        detection.Summary,
	}

	if !opts.DryRun {
		// Single PR-wide comment that carries the `**Code Guru review`
		// substring so the F2 review-once gate (`alreadyReviewed`)
		// catches the next webhook delivery for the same PR. ADO
		// fires both `pullrequest.created` AND `pullrequest.updated`
		// for every new PR; without the marker the second delivery
		// (arriving seconds after the first finishes and releases the
		// dedup lease) finds no marker and re-runs the trivial path —
		// observed live on an internal smoke PR where two pods
		// posted four duplicate approvals between them.
		c.postReviewCompleteAnnotation(ctx, provider, repo, pr.ID, result)
		// Reviewer-panel vote mirrors the text annotation. Body is
		// intentionally empty so this submission does not duplicate
		// the annotation as a separate PR-wide comment on ADO; the
		// vote alone is enough to surface the verdict in the panel.
		c.submitNativeReview(ctx, provider, repo, pr.ID, detection.Verdict, "", opts)
		// Auto-merge gated by `Settings.Trivial.AutoMerge` (env
		// `CODE_GURU_TRIVIAL_AUTO_MERGE=true`) AND the author allowlist
		// (`Settings.Trivial.AutoMergeAllowedAuthors`). Triviality makes a
		// PR eligible; the allowlist decides whether its author is trusted
		// to merge unattended — so a human's docs PR is approved but left
		// for a human to merge, while a trusted automation account's PR
		// (autobump / autoupdate / config refresh) merges on its own.
		// Best-effort: a merge failure logs at warn and the verdict still
		// stands; anyone can complete the merge manually from the UI.
		if detection.Verdict == verdictApprove && opts.TrivialAutoMerge {
			c.maybeAutoMergeTrivial(ctx, provider, repo, pr, opts)
		}
	}

	return result
}

// maybeAutoMergeTrivial applies the author-allowlist gate and then
// either auto-merges the trivial-approved PR or declines and logs why.
// Pulled out of handleTrivialDetection so the branches stay flat (the
// `nestif` linter rejects the inline 3-level form) and so the
// "approved-only, left for a human" decision has one obvious home.
//
// Caller guarantees the verdict is approve and TrivialAutoMerge is on;
// this method only decides the AUTHOR-trust half of the gate.
func (c *ReviewCommand) maybeAutoMergeTrivial(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	pr forgeEntities.PullRequestDetail,
	opts ReviewOptions,
) {
	if !autoMergeAuthorAllowed(pr.Author, opts.TrivialAutoMergeAuthors) {
		logger.Infof(
			"PR #%d: trivial-approve by author %q is not in the auto-merge allowlist; approved only, leaving the merge to a human",
			pr.ID,
			pr.Author,
		)
		return
	}
	if len(opts.TrivialAutoMergeAuthors) == 0 && opts.TrivialBypassPolicy {
		logger.Warnf(
			"PR #%d: auto-merging with policy bypass but NO author allowlist is configured — every trivial PR from any author "+
				"(including humans) will be force-merged past branch policies; set CODE_GURU_TRIVIAL_AUTO_MERGE_AUTHORS to restrict to trusted automation accounts",
			pr.ID,
		)
	}
	c.autoMergeTrivial(
		ctx, provider, repo, pr.ID,
		opts.TrivialMergeStrategy, opts.TrivialBypassPolicy, opts.TrivialDeleteSourceBranch,
	)
}

// autoMergeAuthorAllowed reports whether a trivial-approved PR may be
// auto-merged given its author and the configured allowlist.
//
// An EMPTY allowlist preserves the historical behaviour — any author's
// trivial PR is eligible — for backward compatibility. Operators are
// nonetheless encouraged to set one, because auto-merging every trivial
// PR regardless of who opened it (especially with policy bypass) force-
// merges human PRs past `Required reviewers`. A docs-only diff is not
// inherently safe: prose can carry a malicious install command, a
// poisoned package name, a phishing link, or leaked secrets, and the
// bypass means no human ever looks. The valuable, low-risk case is
// trusted mechanical automation (autobump / autoupdate / config refresh),
// so the allowlist names exactly those accounts.
//
// When the allowlist is non-empty the PR author must match one entry,
// compared case-insensitively after trimming surrounding whitespace. The
// entries are full account identities matched against the provider's
// `PullRequestDetail.Author` (the login on GitHub, the createdBy display
// name on Azure DevOps), so an operator pins the exact account each
// automation tool opens its PRs under.
func autoMergeAuthorAllowed(author string, allowed []string) bool {
	if len(allowed) == 0 {
		return true
	}
	trimmedAuthor := strings.TrimSpace(author)
	for _, a := range allowed {
		if strings.EqualFold(trimmedAuthor, strings.TrimSpace(a)) {
			return true
		}
	}
	return false
}

// autoMergeTrivial completes the PR via the underlying provider after
// a trivial-approve verdict. `strategy` maps to gitforge's
// `MergePullRequest` strategy string (`"merge"` / `"squash"` /
// `"rebase"` / `"rebaseMerge"`); empty falls back to the platform
// default.
//
// `bypassPolicy` is opt-in via `Settings.Trivial.BypassPolicy`
// (env `CODE_GURU_TRIVIAL_BYPASS_POLICIES`). When true, the bot
// passes `gitforge.WithBypassPolicy(...)` so ADO skips branch
// policies (`Required reviewers`, `Minimum approver count`) that
// would otherwise reject the merge with
// `GitPullRequestUpdateRejectedByPolicyException`. The bot's
// identity must hold the platform-level
// `Bypass policies when completing pull requests` permission for
// the bypass to take effect; without it ADO still returns 403.
// Bypass is kept as a separate flag (rather than baked into
// AutoMerge) so deployments where the bot has merge permission but
// NOT bypass permission still benefit from polite-merge auto-
// completion, instead of every auto-merge attempt becoming a
// hard 403. On GitHub the option is a no-op — bypass there is
// governed by the authenticated user's permission model rather than
// a per-call flag.
//
// `deleteSourceBranch` (default ON via `Settings.Trivial.DeleteSourceBranchEnabled`)
// adds `gitforge.WithDeleteSourceBranch(...)` so the source branch is removed
// after the merge completes — Azure DevOps deletes it server-side on the
// completion call, GitHub deletes the head ref afterwards. Deletion is
// best-effort inside the provider: it never fails the merge, so a leftover
// branch never turns a successful auto-merge into a reported failure.
func (c *ReviewCommand) autoMergeTrivial(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	strategy string,
	bypassPolicy bool,
	deleteSourceBranch bool,
) {
	logger.Infof(
		"PR #%d: auto-merging (strategy=%q, bypass=%v, delete-source-branch=%v) per trivial PR policy",
		prID, strategy, bypassPolicy, deleteSourceBranch,
	)

	var mergeOpts []forgeEntities.MergeOption
	if bypassPolicy {
		mergeOpts = append(mergeOpts, forgeEntities.WithBypassPolicy("auto-merged by code-guru trivial PR policy"))
	}
	if deleteSourceBranch {
		mergeOpts = append(mergeOpts, forgeEntities.WithDeleteSourceBranch())
	}
	if err := provider.MergePullRequest(ctx, repo, prID, strategy, mergeOpts...); err != nil {
		logger.Warnf("PR #%d: auto-merge failed: %v -- the trivial-approve verdict still stands", prID, err)
	}
}

// forgeFileContentFetcherAdapter adapts a ReviewProvider (type-asserted to
// FileAccessProvider) into the domain FileContentFetcher interface.
// This lives here in the command layer because it bridges the provider
// passed to Execute with the domain interface.
type forgeFileContentFetcherAdapter struct {
	provider forgeEntities.FileAccessProvider
	repo     forgeEntities.Repository
}

func (a *forgeFileContentFetcherAdapter) GetFileContent(ctx context.Context, path string) (string, error) {
	return a.provider.GetFileContent(ctx, a.repo, path)
}

func (a *forgeFileContentFetcherAdapter) HasFile(ctx context.Context, path string) bool {
	return a.provider.HasFile(ctx, a.repo, path)
}

func (c *ReviewCommand) buildDiffs(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	files []forgeEntities.PullRequestFile,
) ([]entities.FileDiff, error) {
	var diffs []entities.FileDiff
	for _, f := range files {
		diffs = append(diffs, entities.FileDiff{
			Path:     f.Path,
			Diff:     f.Patch,
			Language: support.ClassifyFile(f.Path),
		})
	}

	// fallback: if all patches are empty (e.g. Azure DevOps), fetch the full unified diff
	if allDiffsEmpty(diffs) {
		logger.Debugf("no per-file patches available, fetching full unified diff for PR #%d", prID)
		fullDiff, err := provider.GetPullRequestDiff(ctx, repo, prID)
		if err != nil {
			return nil, fmt.Errorf("failed to get PR diff: %w", err)
		}

		// `support.LookupChunkByPath` normalises the leading slash
		// that Azure DevOps's `GetPullRequestFiles` returns on every
		// path (e.g. `/README.md`). Without the normalisation, the
		// chunk lookup would always miss for ADO PRs and the AI would
		// receive an empty diff under each file header, then correctly
		// report "no diff to review".
		chunks := support.SplitUnifiedDiff(fullDiff)
		for i := range diffs {
			if chunk, ok := support.LookupChunkByPath(chunks, diffs[i].Path); ok {
				diffs[i].Diff = chunk
			}
		}
	}

	return diffs, nil
}

// postReviewFailedAnnotation drops a single PR-wide notice when the
// AI step crashes, so the PR author understands the silence after
// the "reviewing" marker is a failure rather than the bot still
// working. Without this notice (and combined with the dedup gate
// from PR #100 — only one pod attempts the review per PR), a
// `claude CLI failed: exit status 1` left the PR with the marker
// promising a review that never arrives.
//
// The `reviewErr` is included in summary form so an operator
// glancing at the PR thread immediately knows whether to look at
// the pod logs (claude crash) vs. retry (transient outage). The
// raw error is truncated and quoted via `support.TruncateForLog`
// to bound the rendered body and prevent any provider-side parser
// breaking on a multi-megabyte error from a runaway claude.
//
// Best-effort: a failure to post the annotation logs at `Warn` and
// the original `reviewErr` still bubbles up to the worker via
// `Execute`'s return — the annotation is operator-visible UX, not
// a correctness gate.
func (c *ReviewCommand) postReviewFailedAnnotation(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	reviewErr error,
	sizeCtx reviewFailureContext,
) {
	body := buildReviewFailedBody(time.Now().UTC(), reviewErr, sizeCtx)
	logger.Infof("PR #%d: posting 'review failed' annotation", prID)

	timeoutCtx, cancel := context.WithTimeout(ctx, reviewingMarkerPostTimeout)
	defer cancel()
	if err := provider.PostPullRequestComment(
		timeoutCtx,
		repo,
		prID,
		body,
		forgeEntities.WithThreadStatus(annotationThreadStatus),
	); err != nil {
		logger.Warnf("PR #%d: failed to post 'review failed' annotation: %v", prID, err)
	}
}

// reviewFailureContext carries the SCALE of the reviewed change so the
// "review failed" annotation can quantify HOW MUCH is too much when the
// failure is a context-window overflow. The zero value means "scale unknown",
// in which case the annotation omits the figures and reads exactly as it did
// before — so a failure on a path that never populated it is unaffected.
type reviewFailureContext struct {
	// FileCount is the number of changed files in the pull request.
	FileCount int
	// DiffBytes is the total size, in bytes, of the assembled per-file diffs
	// that were sent to the AI backend.
	DiffBytes int
}

// reviewFailureContextFrom measures the reviewed change from the file list and
// the assembled diffs so the too-large annotation can report the PR's scale.
func reviewFailureContextFrom(
	files []forgeEntities.PullRequestFile, diffs []entities.FileDiff,
) reviewFailureContext {
	total := 0
	for i := range diffs {
		total += len(diffs[i].Diff)
	}

	return reviewFailureContext{FileCount: len(files), DiffBytes: total}
}

// leadSentence renders the sentence that opens both too-large notices — the
// "review failed" annotation (`buildContextWindowFailedBody`) and the
// "reviewing in batches" notice (`buildBatchedReviewNoticeBody`). When the
// scale is known it is stated ("It changes **N files** (~X of diff), which is
// more than the AI reviewer can read in a single pass"); the diff size is
// dropped when only the file count was measured. When the scale is unknown
// the caller's `unknownScale` phrasing is returned verbatim, so each notice
// keeps its own wording for that case. Shared so the two notices quantify the
// change identically and a wording fix lands in both at once.
func (fc reviewFailureContext) leadSentence(unknownScale string) string {
	if fc.FileCount <= 0 {
		return unknownScale
	}

	scale := fmt.Sprintf("**%d %s**", fc.FileCount, pluralizeFiles(fc.FileCount))
	if fc.DiffBytes > 0 {
		scale += fmt.Sprintf(" (~%s of diff)", humanizeBytes(fc.DiffBytes))
	}

	return fmt.Sprintf(
		"It changes %s, which is more than the AI reviewer can read in a single pass",
		scale,
	)
}

// buildReviewFailedBody renders the PR-wide failure notice, posted only
// after the AI backend has failed every retry attempt (see the
// `RetryingAIReviewer` decorator). Pure function — exposed via
// `export_test.go` so the formatting contract is unit-testable without a
// stub provider. Forces UTC inside the helper for the same reason
// `buildReviewingMarkerBody` does (per Copilot review on PR #102).
//
// The body carries only a SHORT, classified reason — never the raw error.
// A transient backend failure embeds the model's raw output (e.g. the
// claude CLI's multi-kilobyte JSON error envelope with its "API Error:
// socket connection closed" message), and echoing that into the PR thread
// is exactly the leak this rewrite removes. The full error is logged by
// the worker for operator diagnosis instead.
//
// A context-window overflow is dispatched to `buildContextWindowFailedBody`:
// it is a distinct failure with a distinct remedy, and the generic body's
// "push a new commit to retry" advice is actively wrong for it (a bigger diff
// only makes it worse).
func buildReviewFailedBody(now time.Time, reviewErr error, sizeCtx reviewFailureContext) string {
	if errors.Is(reviewErr, support.ErrContextWindowExceeded) {
		return buildContextWindowFailedBody(now, sizeCtx)
	}
	if errors.Is(reviewErr, support.ErrContentSafetyRefusal) {
		return buildContentSafetyRefusalBody(now, reviewErr)
	}
	if errors.Is(reviewErr, support.ErrArgumentListTooLong) {
		return buildArgumentLimitFailedBody(now)
	}

	return fmt.Sprintf(
		"\xe2\x9a\xa0\xef\xb8\x8f **Code Guru review failed.**\n\n"+
			"%s, so no review could be posted. This is usually transient — push a new commit or "+
			"mention `@code-guru` in a comment to try again. Diagnostic details are in the bot logs "+
			"(the raw model output is intentionally not posted here).\n\n"+
			"_Failed at %s._",
		classifyReviewFailure(reviewErr),
		now.UTC().Format(time.RFC3339),
	)
}

// buildContextWindowFailedBody renders the notice for the one failure class
// the generic body must NOT cover: the pull request is larger than the AI
// model's context window. It names the real cause and gives the CORRECT next
// steps (split the PR, drop generated/lock files) instead of the generic
// "usually transient — push a new commit" advice, which is wrong here. When
// the change's scale is known (`sizeCtx`) it is stated so the author sees
// exactly how much is too much; a zero-value context omits the figures. Like
// the other annotation bodies it forces UTC and never echoes raw model output.
func buildContextWindowFailedBody(now time.Time, sizeCtx reviewFailureContext) string {
	lead := sizeCtx.leadSentence("It is larger than the AI reviewer can read in a single pass")

	// The headline MUST carry the `**Code Guru review` substring
	// (`support.botReviewCompleteMarker`) so this notice sets the
	// review-once gate — otherwise a too-large PR would be re-reviewed and
	// re-failed on every push, flooding it with duplicate annotations.
	return fmt.Sprintf(
		"\xe2\x9a\xa0\xef\xb8\x8f **Code Guru review couldn't run — "+
			"this pull request is too large for the AI model's context window.**\n\n"+
			"%s, so no review was posted. Retrying — or pushing more commits — will not help, "+
			"because the diff would only grow.\n\n"+
			"To get an automated review, make the change smaller:\n"+
			"- **Split it into several smaller, focused pull requests.** This is the most reliable fix.\n"+
			"- **Exclude generated, vendored, or lock files** (for example `*.lock`, build output, "+
			"`dist/`, snapshots) — they inflate the diff without needing review.\n"+
			"- If the change genuinely cannot be split, review the largest files locally before merging.\n\n"+
			"_Failed at %s._",
		lead,
		now.UTC().Format(time.RFC3339),
	)
}

// buildContentSafetyRefusalBody renders the notice for a content-safety
// refusal: the AI model's safety system declined to review the change (common
// for security-related code). It states the real cause, reassures that this is
// a limitation of the reviewer's safety filters — NOT a judgment that the PR
// is malicious or a defect in the code — and gives the correct next steps (a
// human review; an operator can point Code Guru at a fallback model or a
// different backend). It deliberately does NOT suggest retrying: the same
// content is declined the same way. The provider's coarse category label
// ("cyber", "bio") is surfaced when present; raw model output never is.
//
// The headline carries the `**Code Guru review` review-once marker (see
// buildContextWindowFailedBody) so the notice gates re-review — otherwise a
// refused PR would be re-reviewed and re-refused on every push.
func buildContentSafetyRefusalBody(now time.Time, reviewErr error) string {
	// Map the provider category to a human phrase (Mapper over switch/case);
	// an unknown or empty category falls back to the generic phrasing.
	categoryPhrase := map[string]string{
		"cyber": "flagged this change as cybersecurity-related content",
		"bio":   "flagged this change as biology-related content",
	}
	flagged := "flagged this change"
	if refusal, ok := errors.AsType[*support.ContentSafetyRefusalError](reviewErr); ok {
		if phrase, found := categoryPhrase[refusal.Category]; found {
			flagged = phrase
		}
	}

	return fmt.Sprintf(
		"\xe2\x9a\xa0\xef\xb8\x8f **Code Guru review couldn't run — "+
			"the AI model's content-safety system declined this pull request.**\n\n"+
			"The AI reviewer's safety filters %s and declined to produce a review, so none was posted. "+
			"This is a limitation of the AI model's safety system — it is **not** a judgment that the change "+
			"is malicious or a problem with the code. Retrying or pushing more commits will not change it, "+
			"because the same content is re-evaluated each time.\n\n"+
			"To get this pull request reviewed:\n"+
			"- **Request a review from a human** — the change itself is unaffected.\n"+
			"- An operator can point Code Guru at a **fallback model that handles this content** "+
			"(`ai.anthropic.refusal_fallback_model`) or switch the `ai.backend` — safety-classifier coverage "+
			"varies by model and provider.\n\n"+
			"_Failed at %s._",
		flagged,
		now.UTC().Format(time.RFC3339),
	)
}

// buildArgumentLimitFailedBody renders the notice for a failure the PR author
// can do nothing about: the assembled prompt exceeded the operating system's
// per-argument limit, so the AI backend process was refused before it ever
// started. The oversized part is the SYSTEM prompt — built from the operator's
// rule corpus, not from the diff — which is why this body neither asks the
// author to split the change (the diff is not the problem) nor to push a
// commit (the next exec is refused identically). It names the operator as the
// one who can fix it and points at the knob that does it.
//
// Like the other failure bodies the headline carries the `**Code Guru review`
// substring so the review-once gate is set: without it every push would
// re-run a review the kernel refuses, flooding the PR with identical notices.
func buildArgumentLimitFailedBody(now time.Time) string {
	return fmt.Sprintf(
		"\xe2\x9a\xa0\xef\xb8\x8f **Code Guru review couldn't run — "+
			"the reviewer's own configuration exceeded an operating system limit.**\n\n"+
			"The review instructions Code Guru assembles for this repository grew past the maximum size "+
			"the operating system allows when starting the AI backend, so the backend was never reached "+
			"and no review was posted. **This is not a problem with this pull request** — the diff was "+
			"never read. Retrying or pushing more commits will not change it.\n\n"+
			"This needs an operator:\n"+
			"- **Reduce the configured review rules** (`rules.categories`) so fewer rule files load at once. "+
			"A change touching several languages pulls in every matching language's rules on top of the "+
			"universal set, which is the usual way this limit is reached.\n"+
			"- **Upgrade Code Guru and the Claude CLI** — current versions pass the review instructions "+
			"through a file instead of the command line, which removes this limit entirely.\n\n"+
			"_Failed at %s._",
		now.UTC().Format(time.RFC3339),
	)
}

// humanizeBytes formats a byte count as a compact, human-readable size (e.g.
// "1.8 MB") for the too-large annotation, using base-1024 units.
func humanizeBytes(n int) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := int64(n) / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}

	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// pluralizeFiles returns the singular or plural noun matching a file count.
func pluralizeFiles(n int) string {
	if n == 1 {
		return "file"
	}

	return "files"
}

// classifyReviewFailure maps a review error to a short, human-readable,
// content-free reason for the failure annotation. It does NOT echo the raw
// error: a transient backend failure embeds the AI's raw output, and posting
// that to the PR is the leak `buildReviewFailedBody` exists to avoid. The
// `RetryingAIReviewer` wraps the backend error with `%w`, so `errors.Is`
// still sees `support.ErrUnparseableResponse` through the retry envelope.
func classifyReviewFailure(reviewErr error) string {
	switch {
	case reviewErr == nil:
		return "The AI review could not be completed"
	case errors.Is(reviewErr, support.ErrUnparseableResponse):
		return "The AI did not return a review in the expected JSON format"
	default:
		return "The AI backend errored"
	}
}

// postReviewCompleteAnnotation drops a single PR-wide notice after
// the AI review's inline + summary comments have been posted, so
// the PR author sees an explicit "done" signal that closes out the
// "reviewing" marker (PR #102). Without this, the marker stays
// open-ended and the author has to count comments to infer that
// the bot finished — observed live on `internal-terraform/internal-
// customer-app#NNNN` on `2026-05-01` where the author merged before
// realising the bot had finished and missed the 3 review comments.
//
// The body surfaces the verdict, the count of inline (`Line > 0`)
// comments, and the completion timestamp in the same RFC 3339 UTC
// shape the marker uses (`Started at <ts>`) so a reader can pair
// the marker thread with the completion thread at a glance.
// Best-effort: a failure to post logs at `Warn` and never blocks
// the worker — completion notices are UX, not correctness. Wrapped
// in the same `5s` timeout as the marker so a hung provider cannot
// stall the review pipeline.
//
// Both the log line and the body use `reviewCompletionStats` so a
// nil `result` (defensive — production never passes one, but a
// future refactor might) degrades gracefully instead of panicking
// on `result.Verdict` / `len(result.Comments)` — pinned per
// Copilot review on PR #104 thread `PRRT_kwDOJKAEo85-6Eqy`.
func (c *ReviewCommand) postReviewCompleteAnnotation(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	result *entities.ReviewResult,
) {
	stats := reviewCompletionStats(result)
	body := buildReviewCompleteBody(time.Now().UTC(), result)
	logger.Infof("PR #%d: posting 'review complete' annotation (verdict=%s, inline_comments=%d)",
		prID, stats.verdict, stats.inlineCommentCount)

	timeoutCtx, cancel := context.WithTimeout(ctx, reviewingMarkerPostTimeout)
	defer cancel()
	if err := provider.PostPullRequestComment(
		timeoutCtx,
		repo,
		prID,
		body,
		forgeEntities.WithThreadStatus(annotationThreadStatus),
	); err != nil {
		logger.Warnf("PR #%d: failed to post 'review complete' annotation: %v", prID, err)
	}
}

// completionStats collapses the bits of `*entities.ReviewResult`
// the completion notice cares about (verdict + inline-comment
// count) into a value that is safe to consume even when the
// caller's `result` pointer is nil. Defined as a type so the
// `verdict` / `inlineCommentCount` pair stays self-documenting
// at every call site.
type completionStats struct {
	verdict            string
	inlineCommentCount int
}

// reviewCompletionStats reads the verdict + inline-comment count
// off a `*entities.ReviewResult` defensively. A nil pointer or an
// empty `Verdict` falls back to the canonical `comment` verdict.
// The inline-comment count only counts comments with `Line > 0` —
// PR-wide comments (`Line <= 0`) are repository-wide annotations
// and don't render as "inline threads" in the bot's vocabulary, so
// they shouldn't inflate the "X inline comments" label on the
// completion notice — pinned per Copilot review on PR #104 thread
// `PRRT_kwDOJKAEo85-6ErC`.
func reviewCompletionStats(result *entities.ReviewResult) completionStats {
	stats := completionStats{verdict: verdictComment}
	if result == nil {
		return stats
	}
	if result.Verdict != "" {
		stats.verdict = result.Verdict
	}
	for _, c := range result.Comments {
		if c.Line > 0 {
			stats.inlineCommentCount++
		}
	}
	return stats
}

// buildReviewCompleteBody renders the PR-wide completion notice. Pure
// function — exposed via `export_test.go` so the formatting contract
// is unit-testable without standing up a stub provider. Forces UTC
// inside the helper for the same reason `buildReviewingMarkerBody`
// does (per Copilot review on PR #102 thread `PRRT_kwDOJKAEo85-56Sq`).
// Surfaces the canonical verdict + inline-comment count so the
// author can see the bot's conclusion without scrolling through
// every thread.
func buildReviewCompleteBody(now time.Time, result *entities.ReviewResult) string {
	stats := reviewCompletionStats(result)
	commentsLabel := "comment"
	if stats.inlineCommentCount != 1 {
		commentsLabel = "comments"
	}
	// Surface the verdict's human-readable rationale when present.
	// Mainly for the trivial fast path — every detector emits a
	// `Summary` like `"Documentation-only change detected (...)"` or
	// `"bump-go version bump is incomplete per .autobump.yaml: …"`,
	// and dropping it would leave the PR author with only the
	// verdict label. The LLM path typically leaves Summary empty
	// (the rationale lands as inline comments), so the section is
	// rendered only when there is something to say — preserving the
	// original two-paragraph layout the LLM path has shipped with.
	reasonBlock := ""
	if result != nil && strings.TrimSpace(result.Summary) != "" {
		reasonBlock = result.Summary + "\n\n"
	}
	return fmt.Sprintf(
		"\xe2\x9c\x85 **Code Guru review complete.**\n\n"+
			"Verdict: `%s` \xc2\xb7 %d inline %s.\n\n"+
			"%s"+
			"_Completed at %s._",
		stats.verdict,
		stats.inlineCommentCount,
		commentsLabel,
		reasonBlock,
		now.UTC().Format(time.RFC3339),
	)
}

// postReviewingMarker drops a single PR-wide acknowledgement so the
// author knows the bot has picked up the PR and is doing the work.
// The body is intentionally short — the rich feedback lands as
// inline threads + a summary when the AI completes. The marker
// closes the gap between webhook-receive and review-complete (which
// can be 5-10 minutes on complex diffs), removing the failure mode
// observed on `internal-terraform/internal-customer-app#NNNN` where the
// author merged at the 7-minute mark and missed the 3 well-grounded
// comments that landed 4 minutes later.
//
// Emits an `Info` log line BEFORE the network call so an operator
// can correlate the timestamp shown in the PR thread (`Started at
// <ts>`) with the same `started_at=<ts>` field on the log line, even
// if the provider call hangs or times out and never returns.
//
// Best-effort: the provider call is wrapped in a short
// `context.WithTimeout` so a slow/hung provider cannot stall the
// worker (the marker is UX, not a correctness gate); a failure
// logs at `Warn` and the review continues.
func (c *ReviewCommand) postReviewingMarker(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
) {
	startedAt := time.Now().UTC()
	body := buildReviewingMarkerBody(startedAt)
	logger.Infof(
		"PR #%d: posting 'reviewing' marker (started_at=%s)",
		prID, startedAt.Format(time.RFC3339),
	)

	timeoutCtx, cancel := context.WithTimeout(ctx, reviewingMarkerPostTimeout)
	defer cancel()
	if err := provider.PostPullRequestComment(
		timeoutCtx,
		repo,
		prID,
		body,
		forgeEntities.WithThreadStatus(annotationThreadStatus),
	); err != nil {
		logger.Warnf("PR #%d: failed to post 'reviewing' marker: %v", prID, err)
	}
}

// submitNativeReview records a native PR review (Approved / Changes Requested)
// on the underlying provider so the verdict surfaces in the platform's reviewer
// panel rather than only as text. The text annotation is still posted by the
// caller — this method is purely additive UX, gated by `opts.SubmitNativeReview`
// so existing deployments keep their previous behaviour until operators opt in.
//
// The verdict-to-submission translation lives in `support.MapVerdictToReview`;
// a verdict the mapper does not recognise (e.g. `comment`) returns ok=false and
// this function quietly skips the API call. Failures are logged at `Warn` and
// swallowed: the native review is operator-visible polish, not a correctness
// gate, so a hung provider or a permission failure must not stall the worker
// or cause the surrounding `Execute` to return an error.
func (c *ReviewCommand) submitNativeReview(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	verdict, summary string,
	opts ReviewOptions,
) {
	if !opts.SubmitNativeReview {
		return
	}
	sub, ok := support.MapVerdictToReview(verdict, summary)
	if !ok {
		return
	}
	// Mirror the marker / annotation helpers' timeout: the native review
	// is best-effort UX, so a slow or hung provider must not stall the
	// worker on this path either. `reviewingMarkerPostTimeout` (5s)
	// matches the same SLO used for the surrounding annotation posts so
	// a single review pipeline shares one cap on provider latency.
	timeoutCtx, cancel := context.WithTimeout(ctx, reviewingMarkerPostTimeout)
	defer cancel()
	if err := provider.SubmitPullRequestReview(timeoutCtx, repo, prID, sub); err != nil {
		logger.Warnf(
			"PR #%d: native review submission failed (verdict=%s): %v — text annotation still posted",
			prID, verdict, err,
		)
	}
}

// annotationThreadStatus is the initial thread status used for every
// PR-wide informational annotation the bot posts (the "reviewing"
// marker, the "review complete" notice, and the "review failed"
// notice). Azure DevOps treats `closed` as "the discussion is done"
// — exactly the right shape because none of these threads ask the
// reviewer for action; they exist purely to give the PR author a
// status signal in line with the rest of the comment activity.
// Without this, every successful review left two unresolved
// "informational" threads on the PR (the marker + the completion
// notice) that the author had to dismiss by hand. GitHub's REST
// review surface has no per-comment status and silently ignores the
// option (`forgeEntities.WithThreadStatus` is documented as no-op there),
// so this is effectively ADO-only behaviour with provider-agnostic
// wiring.
const annotationThreadStatus = "closed"

// reviewingMarkerPostTimeout caps how long a single marker post can
// hold the worker. The marker is UX, not a correctness gate, so a
// hung provider must not stall the AI review pipeline behind it.
// `5s` covers the p99 latency we see for ADO `POST .../threads`
// from inside the AKS cluster while staying short enough that a
// genuine outage manifests as a fast `Warn` instead of a wedged
// goroutine.
const reviewingMarkerPostTimeout = 5 * time.Second

// buildReviewingMarkerBody renders the PR-wide marker body. Pure
// function — exposed via `export_test.go` so the formatting contract
// is unit-testable without standing up a stub provider. Forces UTC
// inside the helper (rather than trusting the caller) so the
// "RFC 3339 in UTC" contract holds regardless of the input
// `time.Time`'s `Location` — pinned per Copilot review on PR #102
// thread `PRRT_kwDOJKAEo85-56Sq`.
func buildReviewingMarkerBody(now time.Time) string {
	return fmt.Sprintf(
		"\xf0\x9f\xa4\x96 **Code Guru is reviewing this PR.**\n\n"+
			"Please wait for the review to complete before merging — typically 1-3 minutes for "+
			"small PRs, longer for complex diffs. Comments will be posted as inline threads when "+
			"the review finishes.\n\n_Started at %s._",
		now.UTC().Format(time.RFC3339),
	)
}

func (c *ReviewCommand) postComments(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	result *entities.ReviewResult,
	handledAnchors map[string]struct{},
) {
	// Note: the closed-PR re-check that decides whether to post at
	// all lives in `Execute` so a single `GetPullRequestStatus` call
	// gates BOTH `postComments` and the sibling
	// `postReviewCompleteAnnotation`. Originally added per task
	// `#43` and refined per Copilot review on PR #105 thread
	// `PRRT_kwDOJKAEo85-6f_Y`. The webhook payload's `status` is
	// captured at delivery time, but the AI step can take 5–10
	// minutes on complex diffs (PR `#NNNN` on `2026-05-01` took
	// 8.5 minutes), and the PR may have been merged or abandoned in
	// the meantime — the call site re-check catches that.

	// `result.Summary` is no longer posted here as a standalone thread.
	// `postReviewCompleteAnnotation` (the sibling call from `Execute`)
	// renders the same string inside the completion annotation since
	// PR #124, and posting it here too produced a duplicate PR-wide
	// thread on every clean review (`verdict=approve` with empty
	// `Comments`). The annotation remains the single source of truth
	// for the rationale; clean reviews still leave the visible signal
	// the standalone post used to provide because the annotation
	// includes the verdict line, the inline-comments count, and the
	// summary paragraph.

	comments := c.dropStaleComments(ctx, provider, repo, prID, result.Comments)
	comments = c.dropResolvedAnchorComments(prID, comments, handledAnchors)
	comments = c.dropDuplicateComments(ctx, provider, repo, prID, comments)

	for _, comment := range comments {
		if comment.Line > 0 {
			// `_` discards the new thread ID gitforge returns (post
			// `0.9.7`). The inline comment threads are not currently
			// updated after creation — the marker thread (PR #102) is
			// the only candidate for auto-close, and it goes through
			// `PostPullRequestComment`, not this path. Capturing the
			// ID here is reserved for a future "edit on second push"
			// feature.
			_, err := provider.PostPullRequestThreadComment(
				ctx, repo, prID, comment.FilePath, comment.Line, comment.Body,
			)
			if err != nil {
				logger.Errorf("failed to post inline comment on %s:%d: %v", comment.FilePath, comment.Line, err)
			}
		} else {
			body := fmt.Sprintf("**[%s]** %s: %s", comment.Severity, comment.FilePath, comment.Body)
			if err := provider.PostPullRequestComment(ctx, repo, prID, body); err != nil {
				logger.Errorf("failed to post comment: %v", err)
			}
		}
	}
}

// shouldSkip evaluates the early-return gates that fire BEFORE the
// command fetches files or calls the AI. Returns a non-nil result
// (with the documented `verdict=comment` short-circuit shape) when the
// review must be skipped; returns nil when the normal flow proceeds.
// Pulled out of `Execute` so the method's cognitive complexity stays
// inside the linter's budget — each gate's reasoning lives next to its
// own check instead of being interleaved with the AI pipeline below.
//
// Gate order:
//
//  1. Draft PRs (unless `ai.review_drafts=true`) — drafts are
//     work-in-progress; reviewing them flooded operators with churn.
//  2. Review-once (unless the run was triggered by an `@code-guru`
//     mention) — if the bot already posted a "review complete" /
//     "review failed" marker, follow-up pushes to the same PR are
//     no-ops by design; users that want a re-review say so explicitly
//     via the mention path.
func (c *ReviewCommand) shouldSkip(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	pr forgeEntities.PullRequestDetail,
	opts ReviewOptions,
) *entities.ReviewResult {
	if pr.IsDraft && !opts.ReviewDrafts {
		logger.Infof("PR #%d is a draft, skipping review (set ai.review_drafts=true to override)", pr.ID)
		return &entities.ReviewResult{
			PullRequestURL: pr.URL,
			Verdict:        verdictComment,
			Summary:        "skipped: pull request is a draft",
		}
	}
	if !opts.UserMentioned && c.alreadyReviewed(ctx, provider, repo, pr.ID) {
		logger.Infof("PR #%d already reviewed; skipping (mention @code-guru in a comment to request re-review)", pr.ID)
		return &entities.ReviewResult{
			PullRequestURL: pr.URL,
			Verdict:        verdictComment,
			Summary:        "skipped: pull request has already been reviewed; mention @code-guru in a comment to request re-review",
		}
	}
	return nil
}

// buildConversation walks the PR's existing inline comments and groups
// the bot's prior review threads + every reply into the
// `[]entities.ReviewThread` shape the LLM prompt renders before the
// diff. The walk only fires on the `@code-guru` mention path
// (`opts.UserMentioned == true`); first-pass reviews return nil so the
// prompt is byte-for-byte identical to its pre-conversation shape and
// the LLM's output distribution does not drift on the path where there
// is nothing for the model to read.
//
// Best-effort: a `ListPullRequestComments` failure logs at warn and
// returns nil so the re-review still runs (the LLM just loses the
// dialogue context — it will likely re-emit the same finding, which
// the F3 dedup will then drop). The tighter degraded mode beats the
// alternative of failing the whole review when the comments-list call
// has a transient blip.
func (c *ReviewCommand) buildConversation(
	ctx context.Context,
	lister pullRequestCommentLister,
	repo forgeEntities.Repository,
	prID int,
	opts ReviewOptions,
) []entities.ReviewThread {
	if !opts.UserMentioned {
		return nil
	}
	comments, err := lister.ListPullRequestComments(ctx, repo, prID)
	if err != nil {
		logger.Warnf(
			"PR #%d: ListPullRequestComments failed during re-review conversation walk (%v); proceeding without prior conversation context",
			prID,
			err,
		)
		return nil
	}
	// Identify which account the bot posts under on THIS PR. Two
	// signals, combined: (1) the identities the operator configured via
	// `bot_identities` / CODE_GURU_BOT_IDENTITIES, and (2) the author of
	// the bot's own PR-wide status annotations on this PR
	// (`DetectBotAuthors`, self-detection). The second is what makes the
	// walk work out of the box on deployments that post under a service
	// account whose name does not start with `code-guru`: without it the
	// matcher only recognises the GitHub `code-guru[bot]` shape, the
	// conversation comes back empty, and the LLM re-reviews from scratch
	// — re-posting findings the author has already fixed or rebutted
	// (the exact failure this guard closes).
	identities := append([]string(nil), opts.BotIdentities...)
	identities = append(identities, support.DetectBotAuthors(comments)...)

	// Pass `nil` for liveFiles so EVERY prior bot thread reaches the
	// LLM, including those anchored to files the latest diff no longer
	// touches. Those are precisely the threads that should come back as
	// `outdated` in `thread_resolutions` — auto-closing findings whose
	// code was removed or refactored away is the whole point of the
	// `outdated` verdict, and filtering them out at the conversation
	// stage would silently strip the LLM's chance to classify them.
	// The token cost is intentional: a couple of stale-anchor threads
	// in the prompt is a fair price to pay for the bot being able to
	// auto-close them, vs. the alternative where the user is left
	// dismissing them by hand for the rest of the PR's life.
	threads := support.BuildReviewConversation(comments, support.IsBotAuthor(identities...), nil)
	if len(threads) > 0 {
		logger.Infof(
			"PR #%d: re-review will include %d prior bot thread(s) as LLM conversation context",
			prID,
			len(threads),
		)
	} else if len(comments) > 0 {
		// The PR has comments but none were recognised as the bot's.
		// On a re-review this almost always means the bot posts under
		// an account neither the built-in `code-guru` matcher nor the
		// self-detection recognised — the failure mode that makes the
		// bot re-post the same findings every pass. Surface a hint so
		// the operator can pin the identity rather than silently
		// degrading to a context-free re-review.
		logger.Warnf(
			"PR #%d: re-review found %d existing comment(s) but recognised none as prior bot threads; "+
				"the LLM will not see its earlier findings or the author's replies and may repeat itself. "+
				"If code-guru posts under a custom service account, set its identity via "+
				"`bot_identities` / CODE_GURU_BOT_IDENTITIES so re-reviews can read and resolve prior threads",
			prID,
			len(comments),
		)
	}
	return threads
}

// alreadyReviewed reports whether the bot has already posted a "review
// complete" / "review failed" annotation on the PR. The check fetches
// every PR-wide comment via gitforge's `ListPullRequestComments` and
// scans the bodies for `support.HasCompletedReviewMarker` — the marker
// substring lives on the bodies the bot itself writes, so the check is
// self-contained and needs no per-PR state outside of what the PR
// already carries.
//
// Best-effort: a fetch failure logs at warn and returns false so the
// gate degrades to "process the webhook" — never worse than today's
// no-gate baseline. The narrow `pullRequestCommentLister` interface
// keeps the unit test surface small (a 1-method stub instead of a full
// `forgeEntities.ReviewProvider`).
func (c *ReviewCommand) alreadyReviewed(
	ctx context.Context,
	lister pullRequestCommentLister,
	repo forgeEntities.Repository,
	prID int,
) bool {
	comments, err := lister.ListPullRequestComments(ctx, repo, prID)
	if err != nil {
		logger.Warnf(
			"PR #%d: ListPullRequestComments failed (%v); proceeding with review under the assumption nothing has been posted yet",
			prID,
			err,
		)
		return false
	}
	bodies := make([]string, 0, len(comments))
	for _, comment := range comments {
		bodies = append(bodies, comment.Body)
	}
	return support.HasCompletedReviewMarker(bodies)
}

// pullRequestCommentLister is the narrow subset of
// `forgeEntities.ReviewProvider` that `alreadyReviewed` consumes.
// Defined here so the test can pass a 1-method stub rather than build
// a full `ReviewProvider` for every row.
type pullRequestCommentLister interface {
	ListPullRequestComments(
		ctx context.Context, repo forgeEntities.Repository, prID int,
	) ([]forgeEntities.PullRequestComment, error)
}

// pullRequestStatusGetter is the narrow subset of
// `forgeEntities.ReviewProvider` that `isPullRequestClosed` consumes.
// Defined here so the test can pass a 1-method stub rather than
// build a full `ReviewProvider` for every row.
type pullRequestStatusGetter interface {
	GetPullRequestStatus(ctx context.Context, repo forgeEntities.Repository, prID int) (string, error)
}

// isPullRequestClosed re-checks the PR's status via the provider and
// returns true when the PR is no longer eligible for review comments.
// Best-effort: a fetch failure logs `Warn` and the function returns
// false so the caller proceeds with posting (the bot is never worse
// than today's baseline). The closed-status set covers both Azure
// DevOps' enum (`completed`, `abandoned`) and GitHub's mapped values
// from `gitforge.GetPullRequestStatus` (`closed`, `merged`). The
// comparison is case-/whitespace-tolerant for those known closed
// values; any other status — including `active`, `open`, an empty
// string, or a future enum addition the bot has not been taught
// about — is treated as not closed so posting proceeds under the
// same best-effort contract. Pinned per task `#43` and Copilot
// review on PR #105 thread `PRRT_kwDOJKAEo85-6f_d`.
func isPullRequestClosed(
	ctx context.Context,
	getter pullRequestStatusGetter,
	repo forgeEntities.Repository,
	prID int,
) bool {
	status, err := getter.GetPullRequestStatus(ctx, repo, prID)
	if err != nil {
		logger.Warnf(
			"PR #%d: GetPullRequestStatus failed (%v); proceeding with post under the assumption the PR is still active",
			prID,
			err,
		)
		return false
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "abandoned", "completed", "closed", "merged":
		return true
	default:
		return false
	}
}

// dropDuplicateComments removes any inline comment whose (file, line,
// body-fingerprint) triple is already present on the PR. The fingerprint
// is the first `commentDedupBodyPrefix` bytes of the body — long enough
// that two genuinely different findings on the same line are kept, short
// enough that minor wording drift between runs (e.g. the AI rewords the
// remediation paragraph but the lede stays the same) is treated as a
// duplicate. Without this filter every push triggers a new review and
// every prior inline comment gets a duplicate twin — the operationally
// observed flooding pattern that drove this filter's introduction.
//
// PR-wide comments (`Line <= 0`, posted via `PostPullRequestComment`)
// are NOT deduped here: they are typically the summary or per-issue
// PR-wide annotations, and the F2 review-once gate above already
// suppresses entire follow-up reviews so the only path that lands a
// duplicate PR-wide comment is the explicit `@code-guru` re-review,
// which the user asked for.
//
// Best-effort: a fetch failure logs at warn and falls back to posting
// every comment so the behaviour never regresses below today's
// no-dedup baseline. The narrow `pullRequestCommentLister` interface
// keeps the unit test surface small.
func (c *ReviewCommand) dropDuplicateComments(
	ctx context.Context,
	lister pullRequestCommentLister,
	repo forgeEntities.Repository,
	prID int,
	comments []entities.ReviewComment,
) []entities.ReviewComment {
	if len(comments) == 0 {
		return comments
	}
	existing, err := lister.ListPullRequestComments(ctx, repo, prID)
	if err != nil {
		logger.Warnf(
			"PR #%d: skipping comment dedup, ListPullRequestComments failed: %v — posting all %d comments",
			prID, err, len(comments),
		)
		return comments
	}
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		if e.Line <= 0 || e.FilePath == "" {
			continue
		}
		seen[commentDedupKey(e.FilePath, e.Line, e.Body)] = struct{}{}
	}
	kept := make([]entities.ReviewComment, 0, len(comments))
	dropped := 0
	for _, comment := range comments {
		if comment.Line <= 0 || comment.FilePath == "" {
			kept = append(kept, comment)
			continue
		}
		if _, dup := seen[commentDedupKey(comment.FilePath, comment.Line, comment.Body)]; dup {
			dropped++
			continue
		}
		kept = append(kept, comment)
	}
	if dropped > 0 {
		logger.Infof(
			"PR #%d: dropped %d duplicate inline comment(s) already posted on the PR",
			prID, dropped,
		)
	}
	return kept
}

// commentDedupBodyPrefix bounds how many leading bytes of the body
// participate in the dedup fingerprint. 200 is enough to capture the
// distinguishing lede of a typical review comment ("[high] this could
// be nil-checked when …") while ignoring the remediation paragraph,
// which the AI tends to reword between runs even when the underlying
// finding is the same. A future tuning pass could swap this for a
// normalised hash if false-positive rates climb in production.
const commentDedupBodyPrefix = 200

// commentDedupKey builds the fingerprint used by `dropDuplicateComments`
// to decide whether a proposed comment matches an existing one.
// Exposed via `export_test.go` so the test suite can pin the
// fingerprint shape without driving the full filter.
func commentDedupKey(filePath string, line int, body string) string {
	normalisedPath := normalizeFilePath(filePath)
	prefix := body
	if len(prefix) > commentDedupBodyPrefix {
		prefix = prefix[:commentDedupBodyPrefix]
	}
	return fmt.Sprintf("%s:%d:%s", normalisedPath, line, prefix)
}

// dropStaleComments removes any review comment whose `FilePath` is not in
// the current set of changed files on the PR. The AI's response is built
// from a diff snapshot taken at the start of `Execute`; if the user
// pushes another commit (or rebases / squashes) between that snapshot
// and `postComments`, the AI's findings can reference files the latest
// iteration no longer touches. ADO renders such comments with a
// "this file no longer exists in the latest pull request changes"
// warning — observed live on `internal-app/internal-integrator#NNNN`
// where every bot comment carried that banner because the PR had been
// rewritten between the webhook firing and the review completing.
//
// The check is best-effort: if `GetPullRequestFiles` fails (e.g.
// transient ADO outage) we fall back to posting all comments so the
// behaviour never regresses below today's baseline. The path
// comparison normalises a leading `/` so an AI response with
// `internal/foo.go` matches an ADO-style `/internal/foo.go` (the same
// normalisation `support.LookupChunkByPath` uses on the diff side).
func (c *ReviewCommand) dropStaleComments(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	comments []entities.ReviewComment,
) []entities.ReviewComment {
	if len(comments) == 0 {
		return comments
	}
	files, err := provider.GetPullRequestFiles(ctx, repo, prID)
	if err != nil {
		logger.Warnf(
			"PR #%d: skipping staleness check, GetPullRequestFiles failed: %v — posting all %d comments",
			prID, err, len(comments),
		)
		return comments
	}
	live := make(map[string]struct{}, len(files))
	for _, f := range files {
		live[normalizeFilePath(f.Path)] = struct{}{}
	}
	kept, dropped := filterStaleComments(comments, live)
	if len(dropped) > 0 {
		logger.Warnf(
			"PR #%d: dropped %d stale comment(s) referencing files no longer in the latest iteration: %s",
			prID, len(dropped), summarizeStaleFilePaths(dropped),
		)
	}
	return kept
}

// filterStaleComments partitions `comments` into the ones whose
// `FilePath` is present in `livePaths` and the ones whose path is no
// longer there. Pure function — exposed via `export_test.go` so the
// test suite can pin the contract without standing up a stub
// `forgeEntities.ReviewProvider`.
//
// PR-wide comments (`Line <= 0`, posted via `PostPullRequestComment`)
// and comments with no `FilePath` at all are always kept: they are
// rendered as repository-wide annotations rather than inline threads,
// so they cannot trigger ADO's "file no longer exists" warning even
// if the path string happens to look stale. Only inline comments
// (`Line > 0`) are subject to the staleness drop.
func filterStaleComments(
	comments []entities.ReviewComment,
	livePaths map[string]struct{},
) ([]entities.ReviewComment, []entities.ReviewComment) {
	var kept, dropped []entities.ReviewComment
	for _, comment := range comments {
		if comment.Line <= 0 || comment.FilePath == "" {
			kept = append(kept, comment)
			continue
		}
		if _, ok := livePaths[normalizeFilePath(comment.FilePath)]; ok {
			kept = append(kept, comment)
		} else {
			dropped = append(dropped, comment)
		}
	}
	return kept, dropped
}

// normalizeFilePath strips a leading `/` so AI responses
// (`internal/foo.go`) match ADO-shape paths (`/internal/foo.go`)
// when the staleness filter compares them. Mirrors the same
// normalisation `support.LookupChunkByPath` performs on the diff
// side so both halves of the pipeline use one rule.
func normalizeFilePath(p string) string { return strings.TrimPrefix(p, "/") }

// summarizeStaleFilePaths joins up to the first eight unique dropped
// file paths so the operator log line stays bounded under runs that
// drop a large batch (e.g. a squash that rewrites every file in the
// PR). The trailing "(+N more)" sentinel preserves the count without
// echoing the full list.
//
// Deduplication is keyed on the **normalised** path so an AI response
// that mentions both `internal/foo.go` and `/internal/foo.go` is
// counted once (`normalizeFilePath` strips a single leading `/`,
// matching the rule used on the lookup side). The first form
// encountered is the one printed, so the operator log preserves
// whichever shape the AI actually emitted.
func summarizeStaleFilePaths(dropped []entities.ReviewComment) string {
	const maxShown = 8
	seen := make(map[string]struct{}, len(dropped))
	paths := make([]string, 0, len(dropped))
	for _, c := range dropped {
		key := normalizeFilePath(c.FilePath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		paths = append(paths, c.FilePath)
	}
	if len(paths) <= maxShown {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s (+%d more)", strings.Join(paths[:maxShown], ", "), len(paths)-maxShown)
}

// applyThreadResolutions consumes the LLM's per-prior-thread verdict on
// the mention re-review path. For every entry the bot:
//
//  1. matches the resolution to one of the threads the conversation
//     walker built — primary key is the synthetic `T<n>` id the user
//     prompt rendered next to each thread (`support.ThreadPromptID`);
//     fallback is the file+line anchor for older / malformed LLM
//     responses that drop the id and only when that anchor matches a
//     single thread. Without the id-first match, two prior bot threads
//     anchored to the same `<file>:<line>` would collapse onto one map
//     entry and silently lose every resolution past the first;
//  2. posts a short inline reply on the same anchor so the user sees the
//     bot engaging with the existing thread instead of opening a brand
//     new comment somewhere else;
//  3. for `resolved` and `outdated` verdicts, calls
//     `UpdatePullRequestThreadStatus` so the thread no longer renders as
//     "active / awaiting reviewer" — operationally the difference
//     between "the bot says it is fixed" (which the user has to click
//     to dismiss) and "the bot marked it fixed" (which closes itself).
//
// Returns the set of normalised `(file, line)` anchors the function
// CLOSED (`resolved` / `outdated` only) so the caller's `postComments`
// pass can drop overlapping new `Comments` entries that would otherwise
// land as a duplicate of a finding the bot just marked done. Anchors
// where the resolution was `outstanding` are NOT added: the prior
// thread is still active, and a separate new comment on the same line
// is more likely a distinct finding the LLM wants to surface than a
// duplicate (the prompt forbids restating the same concern via
// `comments`, so a new entry on an `outstanding` anchor must be
// describing a different issue). Suppressing it would be more
// aggressive than the duplicate-guard the dedup gate is meant to be.
//
// Best-effort: a missing thread match logs at debug and skips that
// resolution rather than failing the review (the LLM occasionally
// hallucinates a thread anchor that does not exist in the prompt). A
// reply / status-update failure logs at warn and proceeds — the next
// review run will re-attempt via the same thread_resolutions path.
//
// Returns nil (and a nil set) when there are no resolutions to apply,
// which is the normal first-pass case.
func (c *ReviewCommand) applyThreadResolutions(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	threads []entities.ReviewThread,
	resolutions []entities.ThreadResolution,
) map[string]struct{} {
	if len(resolutions) == 0 || len(threads) == 0 {
		return nil
	}

	// Index threads by their synthetic prompt id (`T1`, `T2`, ...) AND
	// by normalised (file, line) so the resolver can match either way:
	// id-first when the LLM populated it (the only way to disambiguate
	// duplicate anchors), file+line as a defensive fallback for older
	// responses that drop the id. The fallback only fires when the
	// anchor matches exactly one thread — when two threads share an
	// anchor and the LLM did not emit an id, picking either one would
	// be wrong, so the resolution is skipped instead of misrouted.
	byID := make(map[string]entities.ReviewThread, len(threads))
	byAnchor := make(map[string][]entities.ReviewThread, len(threads))
	for idx, t := range threads {
		byID[support.ThreadPromptID(idx)] = t
		anchor := threadAnchorKey(t.FilePath, t.Line)
		byAnchor[anchor] = append(byAnchor[anchor], t)
	}

	handled := make(map[string]struct{}, len(resolutions))
	applied := 0
	for _, res := range resolutions {
		thread, ok := matchResolutionThread(res, byID, byAnchor)
		if !ok {
			logger.Debugf(
				"PR #%d: thread_resolutions entry id=%q %s:%d does not uniquely match any prior bot thread; skipping",
				prID, res.ID, res.FilePath, res.Line,
			)
			continue
		}
		applied++

		// Only suppress overlapping new inline comments when the
		// resolution closes the prior thread — `outstanding` keeps
		// the thread active, so a NEW comment on the same anchor is
		// more likely a separate finding than a duplicate. Marking
		// `handled` BEFORE the network calls below preserves the
		// soft-fail contract: a transient post failure on a closing
		// resolution still suppresses the duplicate comment in
		// `postComments`, because the LLM has already classified the
		// anchor and the next review run will re-attempt the post.
		if shouldCloseResolution(res.Status) {
			handled[threadAnchorKey(thread.FilePath, thread.Line)] = struct{}{}
		}

		body := buildResolutionReplyBody(res)
		if err := c.postResolutionReply(ctx, provider, repo, prID, thread, body); err != nil {
			logger.Warnf(
				"PR #%d: failed to post resolution reply on %s:%d (status=%s): %v",
				prID, thread.FilePath, thread.Line, res.Status, err,
			)
			continue
		}

		if shouldCloseResolution(res.Status) && thread.ThreadID > 0 {
			status := mapResolutionStatusToThreadState(res.Status)
			if err := provider.UpdatePullRequestThreadStatus(
				ctx, repo, prID, int(thread.ThreadID), status,
			); err != nil {
				logger.Warnf(
					"PR #%d: failed to set thread %d on %s:%d to %q: %v — reply was posted, only the auto-close failed",
					prID, thread.ThreadID, thread.FilePath, thread.Line, status, err,
				)
			}
		}
	}

	if applied > 0 {
		logger.Infof(
			"PR #%d: applied %d thread resolution(s) on the re-review path",
			prID, applied,
		)
	}
	return handled
}

// matchResolutionThread routes one ThreadResolution back to the
// conversation thread it refers to. The synthetic id (`T1`, `T2`, ...)
// from the user prompt is the durable key — it is the only thing that
// can disambiguate two prior bot threads on the same `<file>:<line>`.
// When the LLM omits the id (older response, malformed payload), fall
// back to the (file, line) anchor only when it identifies a single
// thread; on a tie we refuse to guess and skip the resolution because
// picking either one would silently misroute a verdict.
func matchResolutionThread(
	res entities.ThreadResolution,
	byID map[string]entities.ReviewThread,
	byAnchor map[string][]entities.ReviewThread,
) (entities.ReviewThread, bool) {
	if res.ID != "" {
		if thread, ok := byID[res.ID]; ok {
			return thread, true
		}
	}
	candidates := byAnchor[threadAnchorKey(res.FilePath, res.Line)]
	if len(candidates) == 1 {
		return candidates[0], true
	}
	return entities.ReviewThread{}, false
}

// resolutionStatus* literals are the canonical strings the LLM emits in
// `ThreadResolution.Status`. Defined as constants so the prompt and the
// post-pipeline cannot drift apart silently — a future tweak to the
// prompt that introduces a fourth state will fail compilation here.
const (
	resolutionStatusResolved    = "resolved"
	resolutionStatusOutstanding = "outstanding"
	resolutionStatusOutdated    = "outdated"
)

// shouldCloseResolution returns true when the resolution status maps to
// a thread state that should auto-close the thread (so the PR author
// does not have to dismiss it by hand). `outstanding` keeps the thread
// `active` — the bot is restating the concern, not closing the loop.
func shouldCloseResolution(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case resolutionStatusResolved, resolutionStatusOutdated:
		return true
	default:
		return false
	}
}

// mapResolutionStatusToThreadState turns the LLM's vocabulary into the
// platform thread-status string expected by gitforge's
// `UpdatePullRequestThreadStatus`. Azure DevOps recognises `"fixed"` /
// `"closed"` / `"active"`; GitHub silently ignores the value (no thread-
// status concept on its REST review surface).
func mapResolutionStatusToThreadState(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case resolutionStatusResolved:
		return "fixed"
	case resolutionStatusOutdated:
		return "closed"
	default:
		return "active"
	}
}

// postResolutionReply posts the re-review verdict for a prior thread. It
// REPLIES INSIDE the existing thread (ReplyToThread) when the thread carries a
// usable provider thread id, so the verdict reads as a continuation of the
// conversation — the bot answering below the author's reply, like a human
// reviewer — instead of a confusing new comment on the same line. It falls
// back to a fresh inline comment at the anchor only when no thread id is
// available (e.g. a provider / edge case where ThreadID is 0), preserving the
// prior behaviour rather than dropping the reply entirely.
func (c *ReviewCommand) postResolutionReply(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	thread entities.ReviewThread,
	body string,
) error {
	if thread.ThreadID > 0 {
		_, err := provider.ReplyToThread(ctx, repo, prID, int(thread.ThreadID), body)
		return err
	}
	_, err := provider.PostPullRequestThreadComment(ctx, repo, prID, thread.FilePath, thread.Line, body)
	return err
}

// buildResolutionReplyBody renders the inline reply the bot posts on
// each prior thread. The body opens with a short status header so the
// PR author can see the bot's verdict at a glance, followed by the
// LLM's `Explanation` so the rationale lives next to the original
// concern rather than scattered across the PR. Falls back to a generic
// "no explanation provided" placeholder when the LLM emits an empty
// `Explanation` (defensive — the prompt asks for one, but malformed
// responses sometimes drop the field).
func buildResolutionReplyBody(res entities.ThreadResolution) string {
	header := resolutionHeader(res.Status)
	explanation := strings.TrimSpace(res.Explanation)
	if explanation == "" {
		explanation = "(no explanation provided)"
	}
	return fmt.Sprintf("%s\n\n%s", header, explanation)
}

// resolutionHeader renders the headline shown above the explanation in
// the reply body. Each status maps to a short, distinct line so the PR
// author can scan a re-reviewed thread and immediately see whether the
// bot considers the concern addressed, still open, or no longer
// applicable.
func resolutionHeader(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case resolutionStatusResolved:
		return "\xe2\x9c\x85 **Resolved by Code Guru re-review.**"
	case resolutionStatusOutstanding:
		return "\xe2\x9a\xa0\xef\xb8\x8f **Still outstanding per Code Guru re-review.**"
	case resolutionStatusOutdated:
		return "\xf0\x9f\x97\x91\xef\xb8\x8f **Outdated per Code Guru re-review.**"
	default:
		return "\xf0\x9f\xa4\x96 **Code Guru re-review note.**"
	}
}

// threadAnchorKey is the normalised string identifier for a thread
// anchor. Mirrors `normalizeFilePath` so leading-`/` ADO paths and
// AI-emitted unprefixed paths collapse onto the same key, which is the
// rule used elsewhere in this file (dedup, staleness filter,
// conversation walker).
func threadAnchorKey(filePath string, line int) string {
	return fmt.Sprintf("%s:%d", normalizeFilePath(filePath), line)
}

// dropResolvedAnchorComments removes any inline comment whose
// `(file, line)` anchor was already addressed by `applyThreadResolutions`.
// This is the second half of the re-review duplicate guard: the LLM is
// instructed not to add a `comments` entry for a finding it classified
// in `thread_resolutions`, but malformed / older responses sometimes do
// both. Without this filter every re-review where the LLM keeps a
// finding `outstanding` would land BOTH a thread-resolution reply AND a
// brand-new inline comment on the same line.
//
// PR-wide comments (`Line <= 0`) and comments with no `FilePath` are
// always kept — `handledAnchors` only carries inline anchors.
func (c *ReviewCommand) dropResolvedAnchorComments(
	prID int,
	comments []entities.ReviewComment,
	handledAnchors map[string]struct{},
) []entities.ReviewComment {
	if len(comments) == 0 || len(handledAnchors) == 0 {
		return comments
	}
	kept := make([]entities.ReviewComment, 0, len(comments))
	dropped := 0
	for _, comment := range comments {
		if comment.Line <= 0 || comment.FilePath == "" {
			kept = append(kept, comment)
			continue
		}
		if _, hit := handledAnchors[threadAnchorKey(comment.FilePath, comment.Line)]; hit {
			dropped++
			continue
		}
		kept = append(kept, comment)
	}
	if dropped > 0 {
		logger.Infof(
			"PR #%d: dropped %d new inline comment(s) whose anchor was already addressed via thread_resolutions",
			prID, dropped,
		)
	}
	return kept
}

func allDiffsEmpty(diffs []entities.FileDiff) bool {
	for _, d := range diffs {
		if d.Diff != "" {
			return false
		}
	}
	return true
}
