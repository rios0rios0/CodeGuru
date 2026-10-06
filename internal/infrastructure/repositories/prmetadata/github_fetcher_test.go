package prmetadata_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/infrastructure/repositories/prmetadata"
)

func TestGitHubFetcherGetPullRequestMetadata(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{Organization: "my-org", Name: "my-repo"}

	t.Run("should return description and commit count when the API responds", func(t *testing.T) {
		t.Parallel()

		// given: a real HTTP server standing in for api.github.com,
		// asserting the exact path and auth header the fetcher sends.
		var gotPath, gotAuth, gotAPIVersion string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			gotAuth = r.Header.Get("Authorization")
			gotAPIVersion = r.Header.Get("X-Github-Api-Version")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"body": "Adds a rate limiter.", "commits": 5, "title": "ignored"}`))
		}))
		defer server.Close()
		fetcher := prmetadata.NewGitHubFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		metadata, err := fetcher.GetPullRequestMetadata(context.Background(), "gh-token", repo, 42)

		// then
		require.NoError(t, err)
		assert.Equal(t, "Adds a rate limiter.", metadata.Description)
		assert.Equal(t, 5, metadata.CommitCount)
		assert.Equal(t, "/repos/my-org/my-repo/pulls/42", gotPath)
		assert.Equal(t, "Bearer gh-token", gotAuth)
		assert.Equal(t, "2022-11-28", gotAPIVersion)
	})

	t.Run("should return an error when the API responds with a non-2xx status", func(t *testing.T) {
		t.Parallel()

		// given
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()
		fetcher := prmetadata.NewGitHubFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "gh-token", repo, 42)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})

	t.Run("should return an error when the API responds with invalid JSON", func(t *testing.T) {
		t.Parallel()

		// given
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>login page</html>"))
		}))
		defer server.Close()
		fetcher := prmetadata.NewGitHubFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "gh-token", repo, 42)

		// then
		require.Error(t, err)
	})

	t.Run("should omit the Authorization header when the token is empty", func(t *testing.T) {
		t.Parallel()

		// given: public repos are readable unauthenticated; an empty
		// `Authorization: Bearer ` header would instead be rejected.
		var gotAuth string
		sawHeader := true
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, sawHeader = r.Header["Authorization"]
			_, _ = w.Write([]byte(`{"body": "", "commits": 1}`))
		}))
		defer server.Close()
		fetcher := prmetadata.NewGitHubFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "", repo, 42)

		// then
		require.NoError(t, err)
		assert.Empty(t, gotAuth)
		assert.False(t, sawHeader, "no Authorization header must be sent for an empty token")
	})
}
