package prmetadata_test

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/infrastructure/repositories/prmetadata"
)

// newADOServer stands in for dev.azure.com, serving the PR resource on
// the base path and the commits collection on the `/commits` suffix.
func newADOServer(t *testing.T, prStatus, commitsStatus int) (*httptest.Server, *[]string) {
	t.Helper()
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/commits") {
			w.WriteHeader(commitsStatus)
			_, _ = w.Write([]byte(`{"count": 4, "value": []}`))
			return
		}
		w.WriteHeader(prStatus)
		_, _ = w.Write([]byte(`{"description": "Bumps the SDK.", "pullRequestId": 42}`))
	}))
	t.Cleanup(server.Close)
	return server, &paths
}

func TestAzureDevOpsFetcherGetPullRequestMetadata(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{
		ID:           "repo-uuid",
		Name:         "my-repo",
		Organization: "my-org",
		Project:      "my-project",
	}

	t.Run("should return description and commit count when both endpoints respond", func(t *testing.T) {
		t.Parallel()

		// given
		server, paths := newADOServer(t, http.StatusOK, http.StatusOK)
		fetcher := prmetadata.NewAzureDevOpsFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		metadata, err := fetcher.GetPullRequestMetadata(context.Background(), "ado-pat", repo, 42)

		// then
		require.NoError(t, err)
		assert.Equal(t, "Bumps the SDK.", metadata.Description)
		assert.Equal(t, 4, metadata.CommitCount)
		require.Len(t, *paths, 2)
		assert.Equal(t,
			"/my-org/my-project/_apis/git/repositories/repo-uuid/pullrequests/42?api-version=7.0",
			(*paths)[0], "the PR resource must be addressed by repository ID when one is known")
		assert.Equal(t,
			"/my-org/my-project/_apis/git/repositories/repo-uuid/pullrequests/42/commits?api-version=7.0",
			(*paths)[1])
	})

	t.Run("should authenticate with the documented empty-username Basic scheme", func(t *testing.T) {
		t.Parallel()

		// given
		var gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			_, _ = w.Write([]byte(`{"description": "", "count": 0}`))
		}))
		t.Cleanup(server.Close)
		fetcher := prmetadata.NewAzureDevOpsFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "ado-pat", repo, 42)

		// then
		require.NoError(t, err)
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte(":ado-pat"))
		assert.Equal(t, expected, gotAuth)
	})

	t.Run("should return description-only metadata when the commits endpoint fails", func(t *testing.T) {
		t.Parallel()

		// given: the commit count is a garnish — its outage must not
		// discard the description that already loaded.
		server, _ := newADOServer(t, http.StatusOK, http.StatusInternalServerError)
		fetcher := prmetadata.NewAzureDevOpsFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		metadata, err := fetcher.GetPullRequestMetadata(context.Background(), "ado-pat", repo, 42)

		// then
		require.NoError(t, err)
		assert.Equal(t, "Bumps the SDK.", metadata.Description)
		assert.Zero(t, metadata.CommitCount, "an unavailable count must read as unknown, not fabricated")
	})

	t.Run("should return an error when the PR resource fetch fails", func(t *testing.T) {
		t.Parallel()

		// given
		server, _ := newADOServer(t, http.StatusUnauthorized, http.StatusOK)
		fetcher := prmetadata.NewAzureDevOpsFetcher(server.Client()).WithBaseURL(server.URL)

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "ado-pat", repo, 42)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("should path-escape the repository name when no ID is known", func(t *testing.T) {
		t.Parallel()

		// given: a repository addressed by display name with a space —
		// the URL must carry the escaped form, mirroring gitforge.
		var gotEscapedPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.HasSuffix(r.URL.Path, "/commits") {
				gotEscapedPath = r.URL.EscapedPath()
			}
			_, _ = w.Write([]byte(`{"description": "", "count": 0}`))
		}))
		t.Cleanup(server.Close)
		fetcher := prmetadata.NewAzureDevOpsFetcher(server.Client()).WithBaseURL(server.URL)
		namedRepo := forgeEntities.Repository{
			Name:         "My Repo",
			Organization: "my-org",
			Project:      "My Project",
		}

		// when
		_, err := fetcher.GetPullRequestMetadata(context.Background(), "ado-pat", namedRepo, 7)

		// then
		require.NoError(t, err)
		assert.Equal(t,
			"/my-org/My%20Project/_apis/git/repositories/My%20Repo/pullrequests/7",
			gotEscapedPath)
	})
}
