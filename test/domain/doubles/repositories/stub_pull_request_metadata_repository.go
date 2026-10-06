package repositories

import (
	"context"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// StubPullRequestMetadataRepository is a hand-rolled double for the
// PR-metadata fetch. It returns a canned metadata value (or error) and
// records how it was called so tests can assert the opt-out gates skip
// the provider call entirely.
type StubPullRequestMetadataRepository struct {
	Metadata entities.PullRequestMetadata
	Err      error

	Calls      int
	LastPRID   int
	LastRepoID string
}

// GetPullRequestMetadata records the call and returns the canned value.
func (r *StubPullRequestMetadataRepository) GetPullRequestMetadata(
	_ context.Context,
	_ forgeEntities.ForgeProvider,
	repo forgeEntities.Repository,
	prID int,
) (entities.PullRequestMetadata, error) {
	r.Calls++
	r.LastPRID = prID
	r.LastRepoID = repo.ID
	return r.Metadata, r.Err
}
