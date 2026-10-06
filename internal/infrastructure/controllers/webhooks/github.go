package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	logger "github.com/sirupsen/logrus"

	"github.com/rios0rios0/codeguru/internal/support"
)

// ghEvent is the minimal subset of a GitHub pull_request webhook payload that
// HandleGitHub needs.
type ghEvent struct {
	Action       string         `json:"action"`
	PullRequest  ghPullRequest  `json:"pull_request"`
	Repository   ghRepository   `json:"repository"`
	Installation ghInstallation `json:"installation"`
}

type ghPullRequest struct {
	Number  int      `json:"number"`
	Title   string   `json:"title"`
	HTMLURL string   `json:"html_url"`
	State   string   `json:"state"`
	Draft   bool     `json:"draft"`
	Head    ghBranch `json:"head"`
	Base    ghBranch `json:"base"`
	User    ghUser   `json:"user"`
}

type ghBranch struct {
	Ref string `json:"ref"`
}

type ghUser struct {
	Login string `json:"login"`
}

type ghRepository struct {
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	HTMLURL  string `json:"html_url"`
	Owner    ghUser `json:"owner"`
}

type ghInstallation struct {
	ID int64 `json:"id"`
}

// supportedGitHubActions lists the pull_request actions HandleGitHub acts on.
//
//nolint:gochecknoglobals // read-only lookup table used as a constant
var supportedGitHubActions = map[string]struct{}{
	"opened":      {},
	"synchronize": {},
	"reopened":    {},
}

// ghIssueCommentEvent is the minimal subset of a GitHub `issue_comment`
// webhook payload that the mention handler needs. GitHub treats every
// PR as an issue, so PR-wide comments fire `issue_comment` (the
// `issue.pull_request` field is set when the underlying issue is a PR
// — absent on regular issue comments, which the handler ignores).
type ghIssueCommentEvent struct {
	Action       string         `json:"action"`
	Comment      ghComment      `json:"comment"`
	Issue        ghIssue        `json:"issue"`
	Repository   ghRepository   `json:"repository"`
	Installation ghInstallation `json:"installation"`
}

type ghComment struct {
	Body string `json:"body"`
	User ghUser `json:"user"`
}

type ghIssue struct {
	Number      int                 `json:"number"`
	Title       string              `json:"title"`
	HTMLURL     string              `json:"html_url"`
	State       string              `json:"state"`
	User        ghUser              `json:"user"`
	PullRequest *ghIssuePullRequest `json:"pull_request"`
}

// ghIssuePullRequest is GitHub's marker that an issue is actually a
// PR. The `url` field is populated when the issue is a PR; the struct
// is non-nil for PR comment events and nil for plain issue comments.
type ghIssuePullRequest struct {
	URL string `json:"url"`
}

// ghReviewCommentEvent is the minimal subset of a GitHub
// `pull_request_review_comment` payload the mention handler needs. This
// is the INLINE counterpart of `ghIssueCommentEvent`: GitHub fires
// `issue_comment` for PR-wide comments and this event for comments
// anchored to a diff line (including replies inside an existing review
// thread), so a mention typed as a thread reply only arrives here.
//
// Unlike `issue_comment`, the payload carries the full `pull_request`
// object — see buildGitHubMentionJob for why most of it is deliberately
// left on the floor.
type ghReviewCommentEvent struct {
	Action       string         `json:"action"`
	Comment      ghComment      `json:"comment"`
	PullRequest  ghPullRequest  `json:"pull_request"`
	Repository   ghRepository   `json:"repository"`
	Installation ghInstallation `json:"installation"`
}

const fullNameSegments = 2

// ghCommentCreatedAction is the only comment action the mention handlers
// act on. Edits and deletions of an old comment must not re-trigger a
// review.
const ghCommentCreatedAction = "created"

// HandleGitHub processes GitHub App webhook events.
//
// Auth: HMAC-SHA256 via the X-Hub-Signature-256 header validated against
// Settings.Server.WebhookSecret. Supported events: pull_request with action
// in {opened, synchronize, reopened}.
//
//nolint:funlen // Single-shot HTTP handler whose length is proportional to its required validation flow.
func (d *Dispatcher) HandleGitHub(w http.ResponseWriter, r *http.Request) {
	if !d.enforceSourceIPAllowlist(w, r, "GitHub") {
		return
	}

	defer func() { _ = r.Body.Close() }()
	body, readErr := io.ReadAll(r.Body)
	if readErr != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}

	if authErr := VerifyHMACSHA256(
		d.settings.Server.WebhookSecret,
		body,
		r.Header.Get("X-Hub-Signature-256"),
	); authErr != nil {
		logger.Warnf("GitHub webhook rejected: %v", authErr)
		status := http.StatusUnauthorized
		if errors.Is(authErr, ErrMissingHeader) {
			status = http.StatusBadRequest
		}
		writeError(w, status, "unauthorized")
		return
	}

	switch eventType := r.Header.Get("X-Github-Event"); eventType {
	case "pull_request":
		// continues below with the existing push-triggered path
	case "issue_comment":
		// Mention handler — dispatched separately because the payload
		// shape differs from `pull_request`. On match it enqueues a job
		// with `UserMentioned=true` so the review-once gate is bypassed;
		// on non-match (no mention, comment on a non-PR issue, action !=
		// created) it returns 204 No Content.
		d.handleGitHubIssueComment(w, r, body)
		return
	case "pull_request_review_comment":
		// Same, for a mention typed inside an inline review thread.
		// Requires the App to subscribe to this event separately from
		// `issue_comment` — without the subscription this arm is dead.
		d.handleGitHubReviewComment(w, r, body)
		return
	default:
		logger.Debugf("GitHub webhook: ignoring event %q", eventType)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	event, ok := parseGitHubEvent(w, body)
	if !ok {
		return
	}

	if _, supported := supportedGitHubActions[event.Action]; !supported {
		logger.Debugf("GitHub webhook: ignoring action %q", event.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	owner, repoName, splitErr := splitFullName(event.Repository.FullName)
	if splitErr != nil {
		writeError(w, http.StatusBadRequest, "invalid repository.full_name")
		return
	}
	if !d.allowedOrganization(owner) {
		logger.Warnf("GitHub webhook: org %q not on allowlist", owner)
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	provider, ok := d.buildGitHubProvider(r.Context(), w, event.Installation.ID)
	if !ok {
		return
	}

	job := buildGitHubJob(provider, owner, repoName, event)

	dedupKey := fmt.Sprintf("gh:%s/%s:%d", owner, repoName, job.PR.ID)
	if d.dedupSeen(r.Context(), dedupKey) {
		logDebugf("GitHub webhook: duplicate delivery for PR #%d in %s/%s — skipping", job.PR.ID, owner, repoName)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, "duplicate")
		return
	}

	job.DedupKey = dedupKey
	if submitErr := d.submitter.Submit(job); submitErr != nil {
		logger.Errorf("GitHub webhook: submit failed: %v", submitErr)
		// Roll back the dedup record so a webhook retry inside the
		// dedup window is not silently dropped — the backend must
		// only retain keys that actually made it onto the worker
		// queue.
		d.dedupForget(r.Context(), dedupKey)
		writeError(w, http.StatusServiceUnavailable, "queue full")
		return
	}

	logInfof("GitHub webhook: enqueued PR #%d in %s/%s", job.PR.ID, owner, repoName)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "accepted")
}

// parseGitHubEvent unmarshals the payload into a ghEvent or writes a 400 response
// and returns ok=false. The caller should return immediately on ok=false.
func parseGitHubEvent(w http.ResponseWriter, body []byte) (ghEvent, bool) {
	var event ghEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return ghEvent{}, false
	}
	return event, true
}

// ghMention is the provider-agnostic slice of a GitHub comment payload
// that submitGitHubMention needs, so the `issue_comment` and
// `pull_request_review_comment` handlers can share one implementation
// of the mention pipeline instead of duplicating it.
type ghMention struct {
	// eventName labels pod logs so an operator can tell which of the two
	// comment events produced a given line.
	eventName string
	// dedupe requests the duplicate-delivery gate. See
	// handleGitHubReviewComment for why only the inline path sets it.
	dedupe       bool
	commentBody  string
	commenter    string
	repository   ghRepository
	installation int64
	prNumber     int
	prTitle      string
	prURL        string
	prAuthor     string
}

// handleGitHubIssueComment processes the `issue_comment` event so a
// user can request a re-review by mentioning the bot in a PR-wide
// comment. The handler:
//
//   - returns 400 on malformed JSON;
//   - returns 204 on actions other than `created` (the bot does not
//     re-trigger on comment edits / deletions);
//   - returns 204 when the comment is on a regular issue (not a PR) —
//     `issue.pull_request` is nil for those;
//
// everything from the mention check onwards is submitGitHubMention.
func (d *Dispatcher) handleGitHubIssueComment(w http.ResponseWriter, r *http.Request, body []byte) {
	var event ghIssueCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return
	}
	if event.Action != ghCommentCreatedAction {
		logger.Debugf("GitHub webhook: ignoring issue_comment action %q", event.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if event.Issue.PullRequest == nil {
		logDebugf("GitHub webhook: ignoring issue_comment on a non-PR issue #%d", event.Issue.Number)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	d.submitGitHubMention(w, r, ghMention{
		eventName: "issue_comment",
		// A PR-wide comment is a single deliberate human action, so it
		// always goes through — see submitGitHubMention.
		dedupe:       true,
		commentBody:  event.Comment.Body,
		commenter:    event.Comment.User.Login,
		repository:   event.Repository,
		installation: event.Installation.ID,
		prNumber:     event.Issue.Number,
		prTitle:      event.Issue.Title,
		prURL:        event.Issue.HTMLURL,
		prAuthor:     event.Issue.User.Login,
	})
}

// handleGitHubReviewComment processes the `pull_request_review_comment`
// event so a mention typed INSIDE an inline review thread requests a
// re-review too. Without it those mentions were dropped on the floor —
// GitHub routes only PR-wide comments through `issue_comment`, while
// Azure DevOps has always covered both through its single comment event.
//
// Returns 400 on malformed JSON and 204 on actions other than `created`;
// the rest is submitGitHubMention.
//
// This path DOES take the dedup gate. One review submission carrying the
// mention in three inline comments fires three separate `created`
// deliveries, and without the gate that is three concurrent LLM reviews
// of the same PR, three sets of thread replies, and three completion
// annotations.
func (d *Dispatcher) handleGitHubReviewComment(w http.ResponseWriter, r *http.Request, body []byte) {
	var event ghReviewCommentEvent
	if err := json.Unmarshal(body, &event); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON")
		return
	}
	if event.Action != ghCommentCreatedAction {
		logger.Debugf("GitHub webhook: ignoring pull_request_review_comment action %q", event.Action)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	d.submitGitHubMention(w, r, ghMention{
		eventName:    "pull_request_review_comment",
		dedupe:       true,
		commentBody:  event.Comment.Body,
		commenter:    event.Comment.User.Login,
		repository:   event.Repository,
		installation: event.Installation.ID,
		prNumber:     event.PullRequest.Number,
		prTitle:      event.PullRequest.Title,
		prURL:        event.PullRequest.HTMLURL,
		prAuthor:     event.PullRequest.User.Login,
	})
}

// submitGitHubMention is the tail both GitHub comment handlers share:
// mention check, bot-self-author guard, allowlist, provider build, and
// submission of a job with `UserMentioned=true` so the review-once gate
// is bypassed on the worker side.
//
// Responses: 204 when the body mentions neither `@code-guru` nor a
// configured bot identity, or when the bot itself authored the comment;
// 400 on an unparsable `repository.full_name`; 403 off-allowlist; 503
// when the queue is full; 202 on success. When `mention.dedupe` is set a
// duplicate delivery answers 200 `duplicate` instead.
//

func (d *Dispatcher) submitGitHubMention(w http.ResponseWriter, r *http.Request, mention ghMention) {
	if !support.HasMention(mention.commentBody, d.settings.BotIdentities...) {
		logDebugf(
			"GitHub webhook: %s on PR #%d mentions neither @code-guru nor a configured bot identity; skipping",
			mention.eventName, mention.prNumber,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Skip comments the bot itself authored: its own annotations carry an
	// "@code-guru ... to try again" line, and re-reviewing on that would
	// loop review->fail->annotate->webhook forever (see the ADO handler).
	// This matters more on the inline path, where a re-review posts one
	// reply per prior thread and each is another delivery.
	if support.IsBotAuthor(d.settings.BotIdentities...)(mention.commenter) {
		logDebugf(
			"GitHub webhook: %s on PR #%d is authored by the bot itself (%s); skipping self-triggered re-review",
			mention.eventName, mention.prNumber, mention.commenter,
		)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	owner, repoName, splitErr := splitFullName(mention.repository.FullName)
	if splitErr != nil {
		writeError(w, http.StatusBadRequest, "invalid repository.full_name")
		return
	}
	if !d.allowedOrganization(owner) {
		logger.Warnf("GitHub webhook: org %q not on allowlist (mention path)", owner)
		writeError(w, http.StatusForbidden, "forbidden")
		return
	}

	provider, ok := d.buildGitHubProvider(r.Context(), w, mention.installation)
	if !ok {
		return
	}

	job := buildGitHubMentionJob(provider, owner, repoName, mention)

	// A distinct key prefix from the PR-lifecycle path's `gh:` so a
	// mention arriving right after a push is not swallowed by it.
	dedupKey := fmt.Sprintf("gh-mention:%s/%s:%d", owner, repoName, mention.prNumber)
	if mention.dedupe {
		if d.dedupSeen(r.Context(), dedupKey) {
			logDebugf("GitHub webhook: duplicate %s mention for PR #%d in %s/%s — skipping",
				mention.eventName, mention.prNumber, owner, repoName)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "duplicate")
			return
		}
		job.DedupKey = dedupKey
	}

	if submitErr := d.submitter.Submit(job); submitErr != nil {
		logger.Errorf("GitHub webhook: submit failed (mention path): %v", submitErr)
		if mention.dedupe {
			// Roll back so a webhook retry inside the dedup window is not
			// silently dropped — the backend must only retain keys that
			// actually made it onto the worker queue.
			d.dedupForget(r.Context(), dedupKey)
		}
		writeError(w, http.StatusServiceUnavailable, "queue full")
		return
	}
	logInfof("GitHub webhook: enqueued %s mention re-review for PR #%d in %s/%s (commenter=%s)",
		mention.eventName, job.PR.ID, owner, repoName, mention.commenter)
	w.WriteHeader(http.StatusAccepted)
	_, _ = fmt.Fprint(w, "accepted")
}

// buildGitHubMentionJob assembles the mention job.
//
// Only ID / Title / URL / Author are carried, matching the ADO mention
// path (`handleADOComment`) exactly. The `pull_request_review_comment`
// payload also offers `draft`, `state`, and the head/base refs, and
// `IsDraft` in particular is left false ON PURPOSE: the draft gate in
// `ReviewCommand.shouldSkip` fires BEFORE the `UserMentioned` check, so
// propagating it would make an explicit mention on a draft PR silently
// do nothing — while the same mention posted PR-wide would work. An
// explicit mention is a deliberate override of the draft gate.
func buildGitHubMentionJob(
	provider forgeEntities.ReviewProvider, owner, repoName string, mention ghMention,
) Job {
	return Job{
		Provider: provider,
		Repo: forgeEntities.Repository{
			Name:         repoName,
			Organization: owner,
			RemoteURL:    mention.repository.HTMLURL,
		},
		PR: forgeEntities.PullRequestDetail{
			PullRequest: forgeEntities.PullRequest{
				ID:    mention.prNumber,
				Title: mention.prTitle,
				URL:   mention.prURL,
			},
			Author: mention.prAuthor,
		},
		UserMentioned: true,
	}
}

// buildGitHubProvider resolves a token (App installation or PAT) and returns the
// configured ReviewProvider, or writes an error response and returns ok=false.
func (d *Dispatcher) buildGitHubProvider(
	ctx context.Context, w http.ResponseWriter, installationID int64,
) (forgeEntities.ReviewProvider, bool) {
	token, err := d.resolveGitHubToken(ctx, installationID)
	if err != nil {
		logger.Errorf("GitHub webhook: token resolution failed: %v", err)
		writeError(w, http.StatusInternalServerError, "token error")
		return nil, false
	}
	provider, err := d.providerRegistry.GetReviewProvider("github", token)
	if err != nil {
		logger.Errorf("GitHub webhook: failed to build provider: %v", err)
		writeError(w, http.StatusInternalServerError, "provider error")
		return nil, false
	}
	return provider, true
}

func buildGitHubJob(provider forgeEntities.ReviewProvider, owner, repoName string, event ghEvent) Job {
	return Job{
		Provider: provider,
		Repo: forgeEntities.Repository{
			Name:         repoName,
			Organization: owner,
			RemoteURL:    event.Repository.HTMLURL,
		},
		PR: forgeEntities.PullRequestDetail{
			PullRequest: forgeEntities.PullRequest{
				ID:     event.PullRequest.Number,
				Title:  event.PullRequest.Title,
				URL:    event.PullRequest.HTMLURL,
				Status: event.PullRequest.State,
			},
			SourceBranch: event.PullRequest.Head.Ref,
			TargetBranch: event.PullRequest.Base.Ref,
			Author:       event.PullRequest.User.Login,
			IsDraft:      event.PullRequest.Draft,
		},
		CIPassed: false,
	}
}

// resolveGitHubToken returns either an App installation token (when the App is
// configured and an installation ID is present) or a configured PAT.
func (d *Dispatcher) resolveGitHubToken(ctx context.Context, installationID int64) (string, error) {
	if d.githubTokenizer != nil && installationID != 0 {
		return d.githubTokenizer.InstallationToken(ctx, installationID)
	}
	if pat := d.findToken("github"); pat != "" {
		return pat, nil
	}
	return "", errors.New("no github_app private key and no github PAT configured")
}

func splitFullName(fullName string) (string, string, error) {
	parts := strings.SplitN(fullName, "/", fullNameSegments)
	if len(parts) != fullNameSegments || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid full_name %q", fullName)
	}
	return parts[0], parts[1], nil
}
