package anthropic_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/infrastructure/repositories/anthropic"
	"github.com/rios0rios0/codeguru/internal/support"
)

func newRequest() entities.ReviewRequest {
	return entities.ReviewRequest{
		PullRequest: forgeEntities.PullRequestDetail{
			PullRequest:  forgeEntities.PullRequest{Title: "test"},
			SourceBranch: "feature",
			TargetBranch: "main",
		},
		Diffs: []entities.FileDiff{{Path: "a.go", Diff: "+x", Language: "go"}},
	}
}

func TestAIReviewerRepositoryName(t *testing.T) {
	t.Parallel()

	t.Run("should return anthropic as backend name", func(t *testing.T) {
		t.Parallel()
		// given
		repo := anthropic.NewAIReviewerRepository("k", "")

		// when
		name := repo.Name()

		// then
		assert.Equal(t, "anthropic", name)
	})
}

func TestReviewDiff(t *testing.T) {
	t.Parallel()

	t.Run("should return summary when API returns a single text block", func(t *testing.T) {
		t.Parallel()
		// given
		server := newAnthropicStub(t, http.StatusOK, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": `{"summary":"lgtm","comments":[]}`},
			},
		})
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		result, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.NoError(t, err)
		assert.Equal(t, "lgtm", result.Summary)
	})

	t.Run("should concatenate multiple text blocks in order", func(t *testing.T) {
		t.Parallel()
		// given
		server := newAnthropicStub(t, http.StatusOK, map[string]any{
			"content": []map[string]any{
				{"type": "text", "text": `{"summary":"par`},
				{"type": "tool_use", "text": "ignored"},
				{"type": "text", "text": `t1+part2","comments":[]}`},
			},
		})
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		result, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.NoError(t, err)
		assert.Equal(t, "part1+part2", result.Summary)
	})

	t.Run("should return error when content has no text blocks", func(t *testing.T) {
		t.Parallel()
		// given
		server := newAnthropicStub(t, http.StatusOK, map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "text": "nope"},
			},
		})
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		result, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "no text content")
	})

	t.Run("should surface API error message when non-2xx response has JSON error payload", func(t *testing.T) {
		t.Parallel()
		// given
		server := newAnthropicStub(t, http.StatusUnauthorized, map[string]any{
			"error": map[string]any{
				"type":    "authentication_error",
				"message": "invalid x-api-key",
			},
		})
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("bad", "m", anthropic.WithEndpoint(server.URL))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "authentication_error")
		assert.Contains(t, err.Error(), "invalid x-api-key")
	})

	t.Run("should truncate plain-text body in error when non-2xx response is not JSON", func(t *testing.T) {
		t.Parallel()
		// given
		body := strings.Repeat("A", 2000)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, body)
		}))
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "status 500")
		assert.Contains(t, err.Error(), "truncated")
		assert.Less(t, len(err.Error()), 1000)
	})

	t.Run("should send canonical headers and model/prompt in request body", func(t *testing.T) {
		t.Parallel()
		// given
		var capturedAPIKey, capturedVersion, capturedContentType, capturedModel string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			capturedAPIKey = req.Header.Get("X-Api-Key")
			capturedVersion = req.Header.Get("Anthropic-Version")
			capturedContentType = req.Header.Get("Content-Type")

			var payload map[string]any
			_ = json.NewDecoder(req.Body).Decode(&payload)
			if m, ok := payload["model"].(string); ok {
				capturedModel = m
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"summary\":\"ok\",\"comments\":[]}"}]}`)
		}))
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("secret-key", "claude-custom", anthropic.WithEndpoint(server.URL))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.NoError(t, err)
		assert.Equal(t, "secret-key", capturedAPIKey)
		assert.Equal(t, "2023-06-01", capturedVersion)
		assert.Equal(t, "application/json", capturedContentType)
		assert.Equal(t, "claude-custom", capturedModel)
	})
}

func TestReviewDiffContextWindow(t *testing.T) {
	t.Parallel()

	t.Run("should send the 1M context-window beta header when enabled", func(t *testing.T) {
		t.Parallel()
		// given / when
		beta := captureAnthropicBetaHeader(t, anthropic.WithContext1M(true))

		// then
		assert.Equal(t, "context-1m-2025-08-07", beta,
			"enabling the 1M window must send the context-1m beta so large PRs fit in one pass")
	})

	t.Run("should NOT send the beta header when the 1M window is disabled", func(t *testing.T) {
		t.Parallel()
		// given / when
		beta := captureAnthropicBetaHeader(t, anthropic.WithContext1M(false))

		// then
		assert.Empty(t, beta, "the beta header must be absent when the operator opts out")
	})

	t.Run("should NOT send the beta header by default (bare constructor)", func(t *testing.T) {
		t.Parallel()
		// given / when: no WithContext1M option — the factory is the single
		// place that resolves the default-ON toggle, so a bare backend is off.
		beta := captureAnthropicBetaHeader(t)

		// then
		assert.Empty(t, beta)
	})

	t.Run("should classify a 'prompt is too long' 400 as a context-window failure", func(t *testing.T) {
		t.Parallel()
		// given: the real Anthropic too-large 400 shape
		server := newAnthropicStub(t, http.StatusBadRequest, map[string]any{
			"error": map[string]any{
				"type":    "invalid_request_error",
				"message": "prompt is too long: 258000 tokens > 200000 maximum",
			},
		})
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		require.ErrorIs(t, err, support.ErrContextWindowExceeded,
			"a prompt-too-long 400 must carry the sentinel so retries are skipped and the PR gets 'too large' guidance")
		assert.Contains(t, err.Error(), "prompt is too long",
			"the raw provider detail must remain in the (log-only) error for diagnosis")
	})
}

// captureAnthropicBetaHeader runs one successful review against a stub that
// records the outbound Anthropic-Beta header, returning whatever the backend
// sent (empty when it sent none). Keeps the three header-contract tests to a
// single line each.
func captureAnthropicBetaHeader(t *testing.T, opts ...anthropic.Option) string {
	t.Helper()
	var captured string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		captured = req.Header.Get("Anthropic-Beta")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"content":[{"type":"text","text":"{\"summary\":\"ok\",\"comments\":[]}"}]}`)
	}))
	defer server.Close()
	repo := anthropic.NewAIReviewerRepository(
		"k", "m", append([]anthropic.Option{anthropic.WithEndpoint(server.URL)}, opts...)...,
	)
	_, err := repo.ReviewDiff(context.Background(), newRequest())
	require.NoError(t, err)

	return captured
}

func TestReviewDiffContentSafety(t *testing.T) {
	t.Parallel()

	t.Run("should classify stop_reason refusal as a content-safety refusal carrying the category", func(t *testing.T) {
		t.Parallel()
		// given: the model refuses with a `cyber` category; no fallback configured
		server := anthropicModelRouterStub(t, "m", "cyber")
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m", anthropic.WithEndpoint(server.URL))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		require.ErrorIs(
			t,
			err,
			support.ErrContentSafetyRefusal,
			"a refusal must classify as ErrContentSafetyRefusal so retries are skipped and the PR gets 'declined' guidance",
		)
		var refusal *support.ContentSafetyRefusalError
		require.ErrorAs(t, err, &refusal)
		assert.Equal(t, "cyber", refusal.Category,
			"the policy category must survive so the annotation can name it")
	})

	t.Run("should recover via the fallback model when the primary model refuses", func(t *testing.T) {
		t.Parallel()
		// given: primary model `m` refuses, fallback `safe` produces a review
		server := anthropicModelRouterStub(t, "m", "cyber")
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m",
			anthropic.WithEndpoint(server.URL), anthropic.WithRefusalFallbackModel("safe"))

		// when
		result, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.NoError(t, err, "the fallback model must produce a review after the primary model refuses")
		assert.Equal(t, "ok", result.Summary)
	})

	t.Run("should surface the original refusal when the fallback model also refuses", func(t *testing.T) {
		t.Parallel()
		// given: a server that refuses EVERY model, plus a fallback configured
		server := anthropicModelRouterStub(t, "*", "cyber")
		defer server.Close()
		repo := anthropic.NewAIReviewerRepository("k", "m",
			anthropic.WithEndpoint(server.URL), anthropic.WithRefusalFallbackModel("safe"))

		// when
		_, err := repo.ReviewDiff(context.Background(), newRequest())

		// then
		require.Error(t, err)
		assert.ErrorIs(t, err, support.ErrContentSafetyRefusal,
			"a fallback that also refuses must surface the content-safety refusal, not a different error")
	})

	t.Run(
		"should surface the fallback error (not the refusal) when the fallback fails for a non-refusal reason",
		func(t *testing.T) {
			t.Parallel()
			// given: the primary model `m` refuses; the fallback `safe` hits a
			// transient 500 — a recoverable, non-refusal failure. Mislabelling it a
			// content-safety refusal would post the wrong annotation AND block the
			// retry decorator from recovering it.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				var payload struct {
					Model string `json:"model"`
				}
				_ = json.NewDecoder(req.Body).Decode(&payload)
				w.Header().Set("Content-Type", "application/json")
				if payload.Model == "safe" {
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = io.WriteString(w, `{"error":{"type":"api_error","message":"overloaded, please retry"}}`)

					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"stop_reason":  "refusal",
					"content":      []any{},
					"stop_details": map[string]any{"type": "refusal", "category": "cyber"},
				})
			}))
			defer server.Close()
			repo := anthropic.NewAIReviewerRepository("k", "m",
				anthropic.WithEndpoint(server.URL), anthropic.WithRefusalFallbackModel("safe"))

			// when
			_, err := repo.ReviewDiff(context.Background(), newRequest())

			// then
			require.Error(t, err)
			require.NotErrorIs(t, err, support.ErrContentSafetyRefusal,
				"a non-refusal fallback failure must NOT be mislabelled a content-safety refusal")
			assert.Contains(t, err.Error(), "overloaded",
				"the actual fallback error must surface so the retry decorator can classify and retry it")
		},
	)
}

// anthropicModelRouterStub replies with a content-safety refusal (stop_reason
// "refusal") for requests naming refuseModel ("*" refuses every model), and a
// valid review for any other model — so a test can drive both the refusal path
// and the fallback-model recovery path off one server. When category is
// non-empty it is attached as stop_details.category.
func anthropicModelRouterStub(t *testing.T, refuseModel, category string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(req.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")

		if refuseModel == "*" || payload.Model == refuseModel {
			resp := map[string]any{"stop_reason": "refusal", "content": []any{}}
			if category != "" {
				resp["stop_details"] = map[string]any{"type": "refusal", "category": category}
			}
			_ = json.NewEncoder(w).Encode(resp)

			return
		}
		_, _ = io.WriteString(w,
			`{"stop_reason":"end_turn","content":[{"type":"text","text":"{\"summary\":\"ok\",\"comments\":[]}"}]}`)
	}))
}

func newAnthropicStub(t *testing.T, status int, payload map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(payload)
	}))
}
