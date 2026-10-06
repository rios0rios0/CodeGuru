package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/support"
)

// The description byte budget is operator-configurable
// (`ai.max_pr_description_bytes`) and resolved by
// `entities.AIConfig.PRDescriptionBytes()`, which also documents the
// default. The cut is applied at load time so every backend sees the same
// bounded content; `support.Truncate` appends its sentinel so the model
// can tell the document was cut rather than silently ending.

// prMetadataFetchTimeout caps the provider metadata call. Like the
// project-guidelines fetch, PR metadata is review-quality context, not
// a correctness gate — a hung provider must not stall the review
// pipeline behind a nice-to-have fetch. 10s matches the guidelines
// budget: both are single small REST reads.
const prMetadataFetchTimeout = 10 * time.Second

// loadPullRequestMetadata fetches the PR's author-supplied context —
// its description and commit count — so the AI can judge whether the
// diff actually does what the title, branch name, and description
// claim, and flag scope creep the description never mentions.
//
// The load is skipped — returning the zero value so the prompt stays
// byte-for-byte identical to its pre-metadata shape — when:
//
//   - the operator disabled the feature (`ai.pr_metadata: false`);
//   - no metadata repository is wired (defensive: paths that build the
//     command by hand, like tests, stay valid);
//   - the fetch fails or the provider is unsupported. Best-effort by
//     design: metadata sharpens the review but its absence must never
//     block one, so errors log at debug and the review proceeds.
//
// The description is trimmed and bounded here (not in the fetchers) so
// every provider implementation inherits the same prompt-budget
// guarantee.
func (c *ReviewCommand) loadPullRequestMetadata(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	opts ReviewOptions,
) entities.PullRequestMetadata {
	if !opts.LoadPullRequestMetadata || c.metadataRepo == nil {
		return entities.PullRequestMetadata{}
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, prMetadataFetchTimeout)
	defer cancel()
	metadata, err := c.metadataRepo.GetPullRequestMetadata(timeoutCtx, provider, repo, prID)
	if err != nil {
		// An unsupported provider and a transient API error are logged
		// the same way on purpose: neither should mark the review or
		// the PR — the prompt simply keeps its metadata-free shape.
		logger.Debugf(
			"PR #%d: no pull request metadata loaded (%v); reviewing without description/commit context",
			prID, err,
		)
		return entities.PullRequestMetadata{}
	}

	// Zero means the caller never wired a budget (hand-built commands,
	// tests): fall back to the shipped default, never to "truncate to
	// nothing".
	descriptionBudget := opts.MaxPRDescriptionBytes
	if descriptionBudget <= 0 {
		descriptionBudget = entities.AIConfig{}.PRDescriptionBytes()
	}
	metadata.Description = support.Truncate(strings.TrimSpace(metadata.Description), descriptionBudget)
	if metadata.CommitCount > 0 || metadata.Description != "" {
		// CommitCount 0 means "unknown" (e.g. the ADO commits endpoint
		// failed and the fetcher degraded to description-only), not an
		// empty PR — log it as such so an operator reading the line is
		// not misled into thinking the provider reported zero commits.
		commitPart := "commit count unknown"
		if metadata.CommitCount > 0 {
			commitPart = fmt.Sprintf("%d commit(s)", metadata.CommitCount)
		}
		logger.Infof(
			"PR #%d: loaded pull request metadata (%s, %d byte(s) of description)",
			prID, commitPart, len(metadata.Description),
		)
	}
	return metadata
}
