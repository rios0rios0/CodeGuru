// Package prmetadata fetches author-supplied pull request metadata —
// description and commit count — that gitforge's PullRequestDetail does
// not carry. Each supported vendor gets its own small REST fetcher; the
// registry dispatches on the gitforge provider name (Mapper pattern, so
// adding a vendor is adding a map entry, not editing a switch).
//
// The package intentionally talks to the provider REST APIs directly
// (mirroring the webhook ADO hydrator and the GitHub App token
// exchange) instead of extending gitforge: the data is review-context
// only, best-effort by contract, and does not need to round-trip
// through the shared provider abstraction to be useful.
package prmetadata

import (
	"context"
	"fmt"
	"net/http"
	"time"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// fetchTimeout caps each vendor REST call. The review command applies
// its own overall deadline on top; this client-level timeout is the
// backstop for callers that forget one.
const fetchTimeout = 10 * time.Second

// maxResponseBytes bounds how much of a vendor API response is read
// into memory. The metadata payloads are a few KiB of JSON; 4 MiB
// leaves generous headroom (huge generated PR descriptions) while
// guaranteeing a misbehaving endpoint cannot balloon the process.
const maxResponseBytes = 4 * 1024 * 1024

// Provider names as registered in the gitforge provider registry.
// Duplicated here (rather than imported from the vendor packages) so
// this package depends only on gitforge's entity types.
const (
	providerNameGitHub      = "github"
	providerNameAzureDevOps = "azuredevops"
)

// vendorFetcher is the per-vendor seam behind the registry. The token
// is passed per call because webhook-server deployments mint fresh
// provider credentials per delivery (GitHub App installation tokens).
type vendorFetcher interface {
	GetPullRequestMetadata(
		ctx context.Context,
		token string,
		repo forgeEntities.Repository,
		prID int,
	) (entities.PullRequestMetadata, error)
}

// RegistryPullRequestMetadataRepository implements
// repositories.PullRequestMetadataRepository by mapping the gitforge
// provider name onto the vendor fetcher that knows that platform's
// REST shape.
type RegistryPullRequestMetadataRepository struct {
	fetchers map[string]vendorFetcher
}

// NewRegistryPullRequestMetadataRepository builds the registry with the
// production fetcher set (GitHub + Azure DevOps) sharing one HTTP
// client.
func NewRegistryPullRequestMetadataRepository() *RegistryPullRequestMetadataRepository {
	client := &http.Client{Timeout: fetchTimeout}
	return &RegistryPullRequestMetadataRepository{
		fetchers: map[string]vendorFetcher{
			providerNameGitHub:      NewGitHubFetcher(client),
			providerNameAzureDevOps: NewAzureDevOpsFetcher(client),
		},
	}
}

// GetPullRequestMetadata dispatches to the vendor fetcher registered
// for the provider's name. Unknown providers (e.g. a future gitforge
// vendor this package has not learned yet) return an error the caller
// treats as "metadata not available".
func (r *RegistryPullRequestMetadataRepository) GetPullRequestMetadata(
	ctx context.Context,
	provider forgeEntities.ForgeProvider,
	repo forgeEntities.Repository,
	prID int,
) (entities.PullRequestMetadata, error) {
	fetcher, ok := r.fetchers[provider.Name()]
	if !ok {
		return entities.PullRequestMetadata{}, fmt.Errorf(
			"no pull request metadata fetcher registered for provider %q", provider.Name(),
		)
	}
	return fetcher.GetPullRequestMetadata(ctx, provider.AuthToken(), repo, prID)
}
