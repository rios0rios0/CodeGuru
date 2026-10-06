package controllers

import (
	"context"
	"fmt"
	"os"

	configHelpers "github.com/rios0rios0/gitforge/v4/pkg/config/domain/helpers"
	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	registry "github.com/rios0rios0/gitforge/v4/pkg/registry/infrastructure"
	logger "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rios0rios0/codeguru/internal/domain/commands"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
	domainRepos "github.com/rios0rios0/codeguru/internal/domain/repositories"
	infraRepos "github.com/rios0rios0/codeguru/internal/infrastructure/repositories"
	"github.com/rios0rios0/codeguru/internal/infrastructure/repositories/trivial"
	"github.com/rios0rios0/codeguru/internal/support"
)

// ReviewController handles reviewing a single PR by URL.
type ReviewController struct {
	providerRegistry  *registry.ProviderRegistry
	aiReviewerFactory *infraRepos.AIReviewerFactory
	rulesRepoFactory  *infraRepos.RulesRepositoryFactory
	metadataRepo      domainRepos.PullRequestMetadataRepository
}

// NewReviewController creates a new ReviewController.
func NewReviewController(
	providerRegistry *registry.ProviderRegistry,
	aiReviewerFactory *infraRepos.AIReviewerFactory,
	rulesRepoFactory *infraRepos.RulesRepositoryFactory,
	metadataRepo domainRepos.PullRequestMetadataRepository,
) *ReviewController {
	return &ReviewController{
		providerRegistry:  providerRegistry,
		aiReviewerFactory: aiReviewerFactory,
		rulesRepoFactory:  rulesRepoFactory,
		metadataRepo:      metadataRepo,
	}
}

// GetBind returns the Cobra command metadata.
func (c *ReviewController) GetBind() entities.ControllerBind {
	return entities.ControllerBind{
		Use:   "review [pr-url]",
		Short: "Review a single pull request by URL",
	}
}

// Execute performs the review of a single PR.
func (c *ReviewController) Execute(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	if len(args) == 0 {
		logger.Error("PR URL is required")
		return
	}

	prURL := args[0]
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	verbose, _ := cmd.Flags().GetBool("verbose")

	if verbose {
		logger.SetLevel(logger.DebugLevel)
	}

	// parse PR URL
	parsed, err := support.ParsePullRequestURL(prURL)
	if err != nil {
		logger.Errorf("failed to parse PR URL: %v", err)
		return
	}

	// load settings
	settings, err := c.loadSettings(cmd)
	if err != nil {
		logger.Errorf("failed to load settings: %v", err)
		return
	}

	// find token for the provider
	token := c.findToken(settings, parsed.ProviderType)
	if token == "" {
		logger.Errorf("no token configured for provider %q", parsed.ProviderType)
		return
	}

	// get review provider
	reviewProvider, err := c.providerRegistry.GetReviewProvider(parsed.ProviderType, token)
	if err != nil {
		logger.Errorf("failed to get review provider: %v", err)
		return
	}

	// build repo entity
	repo := forgeEntities.Repository{
		Name:         parsed.RepoName,
		Organization: parsed.Organization,
		Project:      parsed.Project,
	}

	// find the specific PR
	prs, err := reviewProvider.ListOpenPullRequests(ctx, repo)
	if err != nil {
		logger.Errorf("failed to list PRs: %v", err)
		return
	}

	var targetPR *forgeEntities.PullRequestDetail
	for i := range prs {
		if prs[i].ID == parsed.PRID {
			targetPR = &prs[i]
			break
		}
	}

	if targetPR == nil {
		logger.Errorf("PR #%d not found or not open in %s/%s", parsed.PRID, parsed.Organization, parsed.RepoName)
		return
	}

	// Use the same helper as the long-lived webhook DI provider so
	// the CLI and `serve` paths cannot drift on what counts as
	// "trivial detection enabled".
	detectorRegistry := trivial.NewDetectorRegistryFromConfig(settings.Trivial)

	// create the review command with settings-based dependencies
	aiReviewer := c.aiReviewerFactory.Create(settings)
	rulesRepo := c.rulesRepoFactory.Create(settings)
	reviewCmd := commands.NewReviewCommand(aiReviewer, rulesRepo, detectorRegistry, c.metadataRepo)

	// TODO: query CI status automatically via provider.GetPullRequestCheckStatus() once gitforge adds this method
	ciPassed := false

	result, err := reviewCmd.Execute(ctx, reviewProvider, repo, *targetPR, commands.ReviewOptions{
		DryRun:                    dryRun,
		Verbose:                   verbose,
		CIPassed:                  ciPassed,
		SubmitNativeReview:        settings.AI.NativeReviewSubmissionEnabled(),
		MaxGuidelinesBytes:        settings.AI.GuidelinesBytes(),
		MaxPRDescriptionBytes:     settings.AI.PRDescriptionBytes(),
		ReviewDrafts:              settings.AI.ReviewDrafts,
		TrivialAutoMerge:          settings.Trivial.AutoMerge,
		TrivialMergeStrategy:      settings.Trivial.MergeStrategy,
		TrivialBypassPolicy:       settings.Trivial.BypassPolicy,
		TrivialAutoMergeAuthors:   settings.Trivial.AutoMergeAllowedAuthors,
		TrivialDeleteSourceBranch: settings.Trivial.DeleteSourceBranchEnabled(),
		BotIdentities:             settings.BotIdentities,
		LoadProjectGuidelines:     settings.AI.ProjectGuidelinesEnabled(),
		LoadPullRequestMetadata:   settings.AI.PullRequestMetadataEnabled(),
		BatchLargeReviews:         settings.AI.BatchLargeReviewsEnabled(),
		MaxReviewBatches:          settings.AI.ReviewBatches(),
	})
	if err != nil {
		logger.Errorf("review failed: %v", err)
		return
	}

	c.printResult(result, dryRun)
}

func (c *ReviewController) loadSettings(cmd *cobra.Command) (*entities.Settings, error) {
	configPath, _ := cmd.Flags().GetString("config")
	backendOverride, _ := cmd.Flags().GetString("backend")
	rulesPathOverride, _ := cmd.Flags().GetString("rules-path")

	settings, err := c.resolveSettings(configPath)
	if err != nil {
		return nil, err
	}

	// apply CLI overrides
	if backendOverride != "" {
		settings.AI.Backend = backendOverride
	}
	if rulesPathOverride != "" {
		settings.Rules.Path = rulesPathOverride
	}

	return settings, nil
}

func (c *ReviewController) resolveSettings(configPath string) (*entities.Settings, error) {
	if configPath != "" {
		return entities.NewSettings(configPath)
	}

	cfgPath, _ := configHelpers.FindConfigFile("code-guru")
	if cfgPath == "" {
		// no config file; try environment variables
		return entities.NewSettingsFromEnv()
	}

	return entities.NewSettings(cfgPath)
}

func (c *ReviewController) findToken(settings *entities.Settings, providerType string) string {
	for _, p := range settings.Providers {
		if p.Type == providerType {
			return p.Token
		}
	}
	// fallback to environment variable
	if token := os.Getenv("CODE_GURU_PROVIDER_TOKEN"); token != "" {
		return token
	}
	return ""
}

func (c *ReviewController) printResult(result *entities.ReviewResult, dryRun bool) {
	if dryRun {
		_, _ = fmt.Fprintln(os.Stdout, "--- DRY RUN (comments not posted) ---")
	}

	// emit verdict for machine parsing
	_, _ = fmt.Fprintf(os.Stdout, "VERDICT:%s\n", result.Verdict)

	if result.Summary != "" {
		_, _ = fmt.Fprintf(os.Stdout, "\nSummary: %s\n", result.Summary)
	}

	if len(result.Comments) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "\nNo issues found.")
		return
	}

	_, _ = fmt.Fprintf(os.Stdout, "\nFound %d comments:\n", len(result.Comments))
	for _, comment := range result.Comments {
		_, _ = fmt.Fprintf(os.Stdout, "  [%s] %s", comment.Severity, comment.FilePath)
		if comment.Line > 0 {
			_, _ = fmt.Fprintf(os.Stdout, ":%d", comment.Line)
		}
		_, _ = fmt.Fprintf(os.Stdout, " - %s\n", comment.Body)
	}
}
