package prmetadata

// This file is an internal test: the fetcher map is an unexported
// construction detail, so the dispatch contract — provider name keys
// the map, provider token flows to the vendor fetcher — is pinned here
// with a hand-rolled fake fetcher instead of exporting a setter that
// production code must never call. Mirrors the precedent set by the
// webhooks package's internal tests.

import (
	"context"
	"testing"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// recordingFetcher is a hand-rolled vendorFetcher double that records
// the token it was dispatched with.
type recordingFetcher struct {
	metadata  entities.PullRequestMetadata
	lastToken string
	calls     int
}

func (f *recordingFetcher) GetPullRequestMetadata(
	_ context.Context,
	token string,
	_ forgeEntities.Repository,
	_ int,
) (entities.PullRequestMetadata, error) {
	f.calls++
	f.lastToken = token
	return f.metadata, nil
}

// stubForgeProvider satisfies forgeEntities.ForgeProvider for the two
// methods the registry consumes; any other call panics by design
// (embedded nil interface), which is exactly what the dispatch
// contract promises — the registry needs nothing but name and token.
type stubForgeProvider struct {
	forgeEntities.ForgeProvider

	name  string
	token string
}

func (p *stubForgeProvider) Name() string      { return p.name }
func (p *stubForgeProvider) AuthToken() string { return p.token }

func TestRegistryGetPullRequestMetadata(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{Name: "demo"}

	t.Run("should dispatch to the fetcher registered for the provider name", func(t *testing.T) {
		t.Parallel()

		// given
		fetcher := &recordingFetcher{
			metadata: entities.PullRequestMetadata{Description: "from github", CommitCount: 2},
		}
		registry := &RegistryPullRequestMetadataRepository{
			fetchers: map[string]vendorFetcher{providerNameGitHub: fetcher},
		}
		provider := &stubForgeProvider{name: "github", token: "tok-123"}

		// when
		metadata, err := registry.GetPullRequestMetadata(context.Background(), provider, repo, 42)

		// then
		require.NoError(t, err)
		assert.Equal(t, "from github", metadata.Description)
		assert.Equal(t, 2, metadata.CommitCount)
		assert.Equal(t, 1, fetcher.calls)
		assert.Equal(t, "tok-123", fetcher.lastToken,
			"the provider's own token must flow to the vendor fetcher")
	})

	t.Run("should return an error when no fetcher is registered for the provider", func(t *testing.T) {
		t.Parallel()

		// given: a vendor this package has not learned (e.g. gitlab) —
		// the caller treats the error as "metadata not available".
		registry := NewRegistryPullRequestMetadataRepository()
		provider := &stubForgeProvider{name: "gitlab", token: "tok"}

		// when
		_, err := registry.GetPullRequestMetadata(context.Background(), provider, repo, 42)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"gitlab"`)
	})

	t.Run("should register production fetchers for github and azuredevops", func(t *testing.T) {
		t.Parallel()

		// given
		registry := NewRegistryPullRequestMetadataRepository()

		// when
		_, githubRegistered := registry.fetchers[providerNameGitHub]
		_, adoRegistered := registry.fetchers[providerNameAzureDevOps]

		// then
		assert.True(t, githubRegistered)
		assert.True(t, adoRegistered)
	})
}
