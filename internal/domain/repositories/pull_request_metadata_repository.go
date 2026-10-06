package repositories

import (
	"context"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// PullRequestMetadataRepository fetches author-supplied pull request
// context (description, commit count) that gitforge's PullRequestDetail
// does not carry. The review command feeds the result to the LLM as an
// "intent" section so the model can judge whether the diff actually
// does what the PR title, branch name, and description claim.
//
// The provider is passed in (rather than captured at construction) for
// the same reason ReviewCommand.Execute receives it: the webhook server
// builds a fresh authenticated provider per delivery (GitHub App
// installation tokens rotate), so the fetcher must resolve the vendor
// and credentials per call. Implementations dispatch on
// provider.Name() and authenticate with provider.AuthToken().
//
// Best-effort contract: callers treat any error as "metadata not
// available" — the review must proceed without it, never fail on it.
type PullRequestMetadataRepository interface {
	// GetPullRequestMetadata returns the description and commit count of
	// a single pull request. An unsupported provider returns an error;
	// partial data (e.g. a description with an unknown commit count) is
	// returned without an error so the prompt can use what exists.
	GetPullRequestMetadata(
		ctx context.Context,
		provider forgeEntities.ForgeProvider,
		repo forgeEntities.Repository,
		prID int,
	) (entities.PullRequestMetadata, error)
}
