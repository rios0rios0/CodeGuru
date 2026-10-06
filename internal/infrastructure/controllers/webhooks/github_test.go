package webhooks_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	configEntities "github.com/rios0rios0/gitforge/v4/pkg/config/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/infrastructure/controllers/webhooks"
	doubles "github.com/rios0rios0/codeguru/test/infrastructure/doubles/repositories"
)

const (
	ghSecret = "gh-test-secret"
	ghOwner  = "rios0rios0"
	ghRepo   = "demo"
)

func defaultGitHubSettings() *entities.Settings {
	return &entities.Settings{
		Providers: []configEntities.ProviderConfig{
			{Type: "github", Token: "ghp_test123"},
		},
		Server: entities.ServerConfig{
			WebhookSecret:        ghSecret,
			AllowedOrganizations: []string{ghOwner},
		},
	}
}

const ghOpenedPayload = `{
  "action": "opened",
  "pull_request": {
    "number": 7,
    "title": "Add new feature",
    "html_url": "https://github.com/rios0rios0/demo/pull/7",
    "state": "open",
    "head": {"ref": "feat/x"},
    "base": {"ref": "main"},
    "user": {"login": "octocat"}
  },
  "repository": {
    "name": "demo",
    "full_name": "rios0rios0/demo",
    "html_url": "https://github.com/rios0rios0/demo",
    "owner": {"login": "rios0rios0"}
  },
  "installation": {"id": 1234}
}`

func githubRequest(t *testing.T, body, eventType string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewBufferString(body))
	req.Header.Set("X-Github-Event", eventType)
	req.Header.Set("X-Hub-Signature-256", computeHMACHeader(ghSecret, body))
	return req
}

func newDispatcherWithGitHubTokenizer(
	t *testing.T,
	settings *entities.Settings,
) (*webhooks.Dispatcher, *doubles.StubWebhookSubmitter) {
	t.Helper()
	d, sub := newDispatcherWithSettings(t, settings)
	d.SetGitHubTokenizer(&doubles.StubGitHubTokenizer{Token: "installation-token-xyz"})
	return d, sub
}

func TestHandleGitHub(t *testing.T) {
	t.Parallel()

	t.Run("should respond 202 (Accepted) when an opened PR is enqueued", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.Equal(t, 7, jobs[0].PR.ID)
		assert.Equal(t, ghRepo, jobs[0].Repo.Name)
		assert.Equal(t, ghOwner, jobs[0].Repo.Organization)
	})

	t.Run("should respond 401 (Unauthorized) when the HMAC is invalid", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewBufferString(ghOpenedPayload))
		req.Header.Set("X-Github-Event", "pull_request")
		req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusUnauthorized, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 400 (Bad Request) when the signature header is missing", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewBufferString(ghOpenedPayload))
		req.Header.Set("X-Github-Event", "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 (No Content) when the action is ignored", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		body := `{"action":"closed","pull_request":{"number":1},"repository":{"full_name":"rios0rios0/demo"},"installation":{"id":1}}`
		req := githubRequest(t, body, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 (No Content) when the event type is unsupported", func(t *testing.T) {
		t.Parallel()

		// given: `push` is genuinely unhandled. This row used to send
		// `issue_comment`, which the dispatcher DOES handle — it passed
		// only because the payload's action is "opened", so it exercised
		// the mention handler's action gate rather than the default arm.
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, ghOpenedPayload, "push")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 400 (Bad Request) when the JSON is malformed", func(t *testing.T) {
		t.Parallel()

		// given
		d, _ := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, `{not json`, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("should respond 403 (Forbidden) when the org is not on the allowlist", func(t *testing.T) {
		t.Parallel()

		// given
		settings := defaultGitHubSettings()
		settings.Server.AllowedOrganizations = []string{"someoneelse"}
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should fall back to the configured PAT when no GitHub App tokenizer is wired", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithSettings(t, defaultGitHubSettings()) // no SetGitHubTokenizer
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusAccepted, w.Code)
		require.Len(t, sub.Jobs(), 1)
	})

	t.Run("should accept a single untyped provider entry as the catch-all PAT", func(t *testing.T) {
		t.Parallel()

		// given - mirrors the env-only configuration where CODE_GURU_PROVIDER_TOKEN
		// populates a single ProviderConfig entry without a Type.
		settings := &entities.Settings{
			Providers: []configEntities.ProviderConfig{
				{Token: "ghp_envtest"}, // no Type set
			},
			Server: entities.ServerConfig{
				WebhookSecret:        ghSecret,
				AllowedOrganizations: []string{ghOwner},
			},
		}
		d, sub := newDispatcherWithSettings(t, settings)
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusAccepted, w.Code)
		require.Len(t, sub.Jobs(), 1)
	})

	t.Run("should respond 403 (Forbidden) when CF-Connecting-IP is outside AllowedSourceCIDRs", func(t *testing.T) {
		t.Parallel()

		// given: source-IP allowlist runs before HMAC verification, so an
		// off-list request never gets a chance to brute-force the signature.
		settings := defaultGitHubSettings()
		settings.Server.AllowedSourceCIDRs = []string{"140.82.112.0/20"} // GitHub Hooks range example
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		req.Header.Set("Cf-Connecting-Ip", "8.8.8.8")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 202 (Accepted) when CF-Connecting-IP is inside AllowedSourceCIDRs", func(t *testing.T) {
		t.Parallel()

		// given
		settings := defaultGitHubSettings()
		settings.Server.AllowedSourceCIDRs = []string{"140.82.112.0/20"}
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		req.Header.Set("Cf-Connecting-Ip", "140.82.112.42")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusAccepted, w.Code)
		assert.Len(t, sub.Jobs(), 1)
	})

	t.Run("should short-circuit a duplicate webhook delivery without enqueueing a second job", func(t *testing.T) {
		t.Parallel()

		// given: GitHub sometimes redelivers a webhook (e.g. on a 5xx
		// response). The dedup cache must accept the first and refuse
		// the second, mirroring the ADO contract. Pinned per Copilot
		// review on PR #100 thread `PRRT_kwDOJKAEo85-5zEz`.
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req1 := githubRequest(t, ghOpenedPayload, "pull_request")
		req2 := githubRequest(t, ghOpenedPayload, "pull_request")
		w1 := httptest.NewRecorder()
		w2 := httptest.NewRecorder()

		// when
		d.HandleGitHub(w1, req1)
		d.HandleGitHub(w2, req2)

		// then
		assert.Equal(t, http.StatusAccepted, w1.Code)
		assert.Equal(t, http.StatusOK, w2.Code, "the duplicate must return 200 (acknowledged) without enqueueing")
		assert.Contains(t, w2.Body.String(), "duplicate")
		assert.Len(t, sub.Jobs(), 1, "exactly one job survives the dedup gate")
	})

	t.Run("should let a webhook retry through after Submit fails (rollback contract)", func(t *testing.T) {
		t.Parallel()

		// given: a submitter wired to fail the first call, succeed
		// the second. Without the rollback in `dedupForget`, the
		// retry inside the TTL would be silently dropped because
		// the cache would still say "duplicate". Pinned per Copilot
		// review on PR #100 thread `PRRT_kwDOJKAEo85-5zE-`.
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		// Wire the dispatcher to a fresh stub that fails the first
		// Submit; we override the default stub here so the saturation
		// behaviour is local to this row.
		failing := doubles.NewStubWebhookSubmitter().WithError(errSubmitterFull)
		d.SetSubmitter(failing)
		req1 := githubRequest(t, ghOpenedPayload, "pull_request")
		w1 := httptest.NewRecorder()

		// when (1): first delivery fails at Submit
		d.HandleGitHub(w1, req1)

		// then (1)
		require.Equal(t, http.StatusServiceUnavailable, w1.Code)
		require.Empty(t, failing.Jobs(), "Submit failed so no job is captured")

		// when (2): retry now hits a healthy submitter — must be allowed through
		d.SetSubmitter(sub)
		req2 := githubRequest(t, ghOpenedPayload, "pull_request")
		w2 := httptest.NewRecorder()
		d.HandleGitHub(w2, req2)

		// then (2)
		assert.Equal(
			t,
			http.StatusAccepted,
			w2.Code,
			"retry within TTL must NOT be dropped just because the previous attempt was recorded",
		)
		assert.Len(t, sub.Jobs(), 1, "the retry reaches the worker queue")
	})
}

const ghDraftOpenedPayload = `{
  "action": "opened",
  "pull_request": {
    "number": 8,
    "title": "WIP: refactor",
    "html_url": "https://github.com/rios0rios0/demo/pull/8",
    "state": "open",
    "draft": true,
    "head": {"ref": "feat/x"},
    "base": {"ref": "main"},
    "user": {"login": "octocat"}
  },
  "repository": {
    "name": "demo",
    "full_name": "rios0rios0/demo",
    "html_url": "https://github.com/rios0rios0/demo",
    "owner": {"login": "rios0rios0"}
  },
  "installation": {"id": 1234}
}`

func TestHandleGitHubPropagatesIsDraft(t *testing.T) {
	t.Parallel()

	// Pin the wiring contract: when GitHub sends `pull_request.draft=true`,
	// the dispatched Job must carry `PR.IsDraft=true` so the downstream
	// ReviewCommand can apply its draft-skip policy. Without this, the
	// `ai.review_drafts=false` default would have no effect on webhook-driven
	// reviews (drafts would still go through the AI path).
	t.Run("should set Job.PR.IsDraft when the webhook payload reports draft=true", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, ghDraftOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.True(t, jobs[0].PR.IsDraft, "draft webhook payload must set PR.IsDraft on the dispatched Job")
	})

	t.Run("should leave Job.PR.IsDraft false when the webhook payload omits draft", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, ghOpenedPayload, "pull_request")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.False(t, jobs[0].PR.IsDraft, "non-draft webhook payload must keep PR.IsDraft=false")
	})
}

func ghIssueCommentPayload(body string) string {
	return `{
  "action": "created",
  "comment": {
    "body": ` + jsonString(body) + `,
    "user": {"login": "felipe"}
  },
  "issue": {
    "number": 12,
    "title": "Add feature X",
    "html_url": "https://github.com/rios0rios0/demo/pull/12",
    "state": "open",
    "user": {"login": "felipe"},
    "pull_request": {"url": "https://api.github.com/repos/rios0rios0/demo/pulls/12"}
  },
  "repository": {
    "name": "demo",
    "full_name": "rios0rios0/demo",
    "html_url": "https://github.com/rios0rios0/demo",
    "owner": {"login": "rios0rios0"}
  },
  "installation": {"id": 1234}
}`
}

// jsonString quotes a string for inline JSON literals; small helper so the
// payload templates above stay readable.
func jsonString(s string) string {
	out := []byte(`"`)
	for _, r := range s {
		switch r {
		case '"':
			out = append(out, '\\', '"')
		case '\\':
			out = append(out, '\\', '\\')
		case '\n':
			out = append(out, '\\', 'n')
		default:
			out = append(out, []byte(string(r))...)
		}
	}
	return string(append(out, '"'))
}

func TestHandleGitHubIssueCommentMention(t *testing.T) {
	t.Parallel()

	t.Run("should enqueue a UserMentioned job when the comment contains @code-guru", func(t *testing.T) {
		t.Parallel()

		// given
		body := ghIssueCommentPayload("@code-guru please re-review the auth changes")
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.True(t, jobs[0].UserMentioned, "mention payload must set Job.UserMentioned=true")
		assert.Equal(t, 12, jobs[0].PR.ID)
	})

	t.Run("should respond 204 and not enqueue when the comment is authored by the bot itself", func(t *testing.T) {
		t.Parallel()

		// given: re-reviewing the bot's own "@code-guru ... to try again"
		// annotation would loop review->fail->annotate->webhook forever.
		// "felipe" is the payload's comment author; pin it as a configured
		// bot identity so the self-author guard recognises it.
		settings := defaultGitHubSettings()
		settings.BotIdentities = []string{"felipe"}
		body := ghIssueCommentPayload("@code-guru re-review")
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs(), "the bot must not re-review its own comment")
	})

	t.Run("should respond 204 when the comment has no @code-guru mention", func(t *testing.T) {
		t.Parallel()

		// given
		body := ghIssueCommentPayload("LGTM, thanks!")
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 when the action is not 'created'", func(t *testing.T) {
		t.Parallel()

		// given: an edited / deleted comment must NOT re-trigger the
		// review even when it contains the mention.
		body := `{"action":"edited","comment":{"body":"@code-guru re-review","user":{"login":"felipe"}},"issue":{"number":12,"pull_request":{"url":"x"}},"repository":{"full_name":"rios0rios0/demo"},"installation":{"id":1}}`
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 when the comment is on a regular issue (not a PR)", func(t *testing.T) {
		t.Parallel()

		// given: a real issue comment (no `pull_request` field on the
		// issue) must NOT trigger the review.
		body := `{"action":"created","comment":{"body":"@code-guru please review","user":{"login":"felipe"}},"issue":{"number":12},"repository":{"full_name":"rios0rios0/demo"},"installation":{"id":1}}`
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should enqueue when the comment mentions a configured bot identity", func(t *testing.T) {
		t.Parallel()

		// given: the deployment posts as a service account, so users
		// @-mention THAT name rather than the built-in `@code-guru`.
		settings := defaultGitHubSettings()
		settings.BotIdentities = []string{"svc-codeguru@corp.example"}
		body := ghIssueCommentPayload("@svc-codeguru please re-review")
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.True(t, jobs[0].UserMentioned)
	})

	t.Run("should respond 204 when the mentioned identity is not configured", func(t *testing.T) {
		t.Parallel()

		// given: same body, but nothing tells the bot it owns that name.
		body := ghIssueCommentPayload("@svc-codeguru please re-review")
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 when the bot mentions its own configured identity", func(t *testing.T) {
		t.Parallel()

		// given: the account name is now BOTH a trigger token and the
		// self-author identity, so the bot quoting its own name must
		// still be skipped or it feeds itself forever. "felipe" is the
		// payload's comment author.
		settings := defaultGitHubSettings()
		settings.BotIdentities = []string{"felipe"}
		body := ghIssueCommentPayload("@felipe re-review")
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, "issue_comment")
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs(), "the bot must not re-review its own comment")
	})
}

func ghReviewCommentPayload(commentBody string, draft bool) string {
	return `{
  "action": "created",
  "comment": {
    "body": ` + jsonString(commentBody) + `,
    "user": {"login": "felipe"}
  },
  "pull_request": {
    "number": 12,
    "title": "Add feature X",
    "html_url": "https://github.com/rios0rios0/demo/pull/12",
    "state": "open",
    "draft": ` + strconv.FormatBool(draft) + `,
    "head": {"ref": "feat/x"},
    "base": {"ref": "main"},
    "user": {"login": "octocat"}
  },
  "repository": {
    "name": "demo",
    "full_name": "rios0rios0/demo",
    "html_url": "https://github.com/rios0rios0/demo",
    "owner": {"login": "rios0rios0"}
  },
  "installation": {"id": 1234}
}`
}

// TestHandleGitHubReviewCommentMention covers the inline review-thread
// mention path. GitHub routes only PR-wide comments through
// `issue_comment`, so before this event was handled a mention typed as a
// reply inside a review thread reached nothing at all.
func TestHandleGitHubReviewCommentMention(t *testing.T) {
	t.Parallel()

	const eventType = "pull_request_review_comment"

	t.Run("should enqueue a UserMentioned job when an inline comment contains @code-guru", func(t *testing.T) {
		t.Parallel()

		// given
		body := ghReviewCommentPayload("@code-guru please re-review this thread", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.True(t, jobs[0].UserMentioned, "mention payload must set Job.UserMentioned=true")
		assert.Equal(t, 12, jobs[0].PR.ID)
		assert.Equal(t, "octocat", jobs[0].PR.Author, "PR author comes from pull_request.user.login")
	})

	t.Run("should enqueue when an inline comment mentions a configured bot identity", func(t *testing.T) {
		t.Parallel()

		// given
		settings := defaultGitHubSettings()
		settings.BotIdentities = []string{"svc-codeguru@corp.example"}
		body := ghReviewCommentPayload("@svc-codeguru please re-review", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		assert.Len(t, sub.Jobs(), 1)
	})

	t.Run("should leave PR.IsDraft false even when pull_request.draft is true", func(t *testing.T) {
		t.Parallel()

		// given: unlike `issue_comment`, this payload carries `draft`.
		// Propagating it would trip the draft gate in
		// `ReviewCommand.shouldSkip`, which runs BEFORE the
		// UserMentioned check — an explicit mention would silently do
		// nothing on a draft PR. Deliberate; pinned so a future "fix"
		// does not reverse it.
		body := ghReviewCommentPayload("@code-guru please re-review", true)
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		require.Equal(t, http.StatusAccepted, w.Code)
		jobs := sub.Jobs()
		require.Len(t, jobs, 1)
		assert.False(t, jobs[0].PR.IsDraft, "an explicit mention overrides the draft-skip gate")
	})

	t.Run("should collapse a burst of inline mentions into one job", func(t *testing.T) {
		t.Parallel()

		// given: one review submission carrying the mention in three
		// inline comments fires three separate `created` deliveries.
		// Without the dedup gate that is three concurrent LLM reviews of
		// the same PR.
		body := ghReviewCommentPayload("@code-guru please re-review", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())

		// when
		for range 3 {
			d.HandleGitHub(httptest.NewRecorder(), githubRequest(t, body, eventType))
		}

		// then
		assert.Len(t, sub.Jobs(), 1, "a burst of inline mentions must enqueue exactly one review")
	})

	t.Run("should respond 204 when the inline comment is authored by the bot itself", func(t *testing.T) {
		t.Parallel()

		// given: a re-review posts one inline reply per prior thread, and
		// each of those is another delivery on THIS event — the
		// self-author guard is the only thing stopping the loop.
		settings := defaultGitHubSettings()
		settings.BotIdentities = []string{"felipe"}
		body := ghReviewCommentPayload("@code-guru re-review", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs(), "the bot must not re-review its own inline reply")
	})

	t.Run("should respond 204 when the inline comment has no mention", func(t *testing.T) {
		t.Parallel()

		// given
		body := ghReviewCommentPayload("nit: rename this variable", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusNoContent, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 204 when the action is not 'created'", func(t *testing.T) {
		t.Parallel()

		for _, action := range []string{"edited", "deleted"} {
			// given: editing or deleting an old inline comment must NOT
			// re-trigger the review even when it contains the mention.
			body := `{"action":"` + action +
				`","comment":{"body":"@code-guru re-review","user":{"login":"felipe"}},` +
				`"pull_request":{"number":12},"repository":{"full_name":"rios0rios0/demo"},"installation":{"id":1}}`
			d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
			req := githubRequest(t, body, eventType)
			w := httptest.NewRecorder()

			// when
			d.HandleGitHub(w, req)

			// then
			assert.Equal(t, http.StatusNoContent, w.Code, action)
			assert.Empty(t, sub.Jobs(), action)
		}
	})

	t.Run("should respond 403 when the org is not on the allowlist", func(t *testing.T) {
		t.Parallel()

		// given
		settings := defaultGitHubSettings()
		settings.Server.AllowedOrganizations = []string{"someone-else"}
		body := ghReviewCommentPayload("@code-guru please re-review", false)
		d, sub := newDispatcherWithGitHubTokenizer(t, settings)
		req := githubRequest(t, body, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusForbidden, w.Code)
		assert.Empty(t, sub.Jobs())
	})

	t.Run("should respond 400 when the JSON is malformed", func(t *testing.T) {
		t.Parallel()

		// given
		d, sub := newDispatcherWithGitHubTokenizer(t, defaultGitHubSettings())
		req := githubRequest(t, `{not json`, eventType)
		w := httptest.NewRecorder()

		// when
		d.HandleGitHub(w, req)

		// then
		assert.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, sub.Jobs())
	})
}
