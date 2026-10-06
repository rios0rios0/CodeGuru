package commands

import (
	"context"
	"fmt"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	registry "github.com/rios0rios0/gitforge/v4/pkg/registry/infrastructure"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// ReviewAll is the interface for the review-all command (batch mode).
type ReviewAll interface {
	Execute(ctx context.Context, settings *entities.Settings, opts ReviewOptions) ([]entities.ReviewResult, error)
}

// ReviewAllCommand orchestrates reviewing all open PRs across configured providers.
type ReviewAllCommand struct {
	providerRegistry *registry.ProviderRegistry
	reviewCommand    Review
}

// NewReviewAllCommand creates a new ReviewAllCommand.
func NewReviewAllCommand(
	providerRegistry *registry.ProviderRegistry,
	reviewCommand Review,
) *ReviewAllCommand {
	return &ReviewAllCommand{
		providerRegistry: providerRegistry,
		reviewCommand:    reviewCommand,
	}
}

// Execute reviews all open PRs across all configured providers and organizations.
func (c *ReviewAllCommand) Execute(
	ctx context.Context,
	settings *entities.Settings,
	opts ReviewOptions,
) ([]entities.ReviewResult, error) {
	var allResults []entities.ReviewResult
	totalErrors := 0

	for _, provCfg := range settings.Providers {
		reviewProvider, err := c.providerRegistry.GetReviewProvider(provCfg.Type, provCfg.Token)
		if err != nil {
			logger.Errorf("failed to initialize provider %q: %v", provCfg.Type, err)
			totalErrors++
			continue
		}

		logger.Infof("processing provider: %s", reviewProvider.Name())

		for _, org := range provCfg.Organizations {
			results, errs := c.processOrganization(ctx, reviewProvider, org, opts)
			allResults = append(allResults, results...)
			totalErrors += errs
		}
	}

	logger.Infof("batch review complete: %d PRs reviewed, %d errors", len(allResults), totalErrors)

	if totalErrors > 0 {
		return allResults, fmt.Errorf("encountered %d errors during batch review", totalErrors)
	}

	return allResults, nil
}

func (c *ReviewAllCommand) processOrganization(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	org string,
	opts ReviewOptions,
) ([]entities.ReviewResult, int) {
	var results []entities.ReviewResult
	errorCount := 0

	logger.Infof("discovering repositories in %q...", org)

	repos, err := provider.DiscoverRepositories(ctx, org)
	if err != nil {
		logger.Errorf("failed to discover repos in %q: %v", org, err)
		return nil, 1
	}

	logger.Infof("found %d repositories in %q", len(repos), org)

	for _, repo := range repos {
		prs, listErr := provider.ListOpenPullRequests(ctx, repo)
		if listErr != nil {
			logger.Errorf("failed to list PRs for %s/%s: %v", repo.Organization, repo.Name, listErr)
			errorCount++
			continue
		}

		if len(prs) == 0 {
			continue
		}

		logger.Infof("%s/%s: %d open PRs", repo.Organization, repo.Name, len(prs))

		for _, pr := range prs {
			result, reviewErr := c.reviewCommand.Execute(ctx, provider, repo, pr, opts)
			if reviewErr != nil {
				logger.Errorf("failed to review PR #%d in %s/%s: %v", pr.ID, repo.Organization, repo.Name, reviewErr)
				errorCount++
				continue
			}
			results = append(results, *result)
		}
	}

	return results, errorCount
}
