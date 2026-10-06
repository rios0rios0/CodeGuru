package controllers

import (
	"context"
	"fmt"
	"os"

	configHelpers "github.com/rios0rios0/gitforge/v4/pkg/config/domain/helpers"
	logger "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rios0rios0/codeguru/internal/domain/commands"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// DiscoverController handles the "discover" subcommand (list open PRs without reviewing).
type DiscoverController struct {
	discoverCommand commands.Discover
}

// NewDiscoverController creates a new DiscoverController.
func NewDiscoverController(discoverCommand commands.Discover) *DiscoverController {
	return &DiscoverController{discoverCommand: discoverCommand}
}

// GetBind returns the Cobra command metadata.
func (c *DiscoverController) GetBind() entities.ControllerBind {
	return entities.ControllerBind{
		Use:   "discover",
		Short: "Discover repositories and list open PRs",
		Long: `Discover repositories from configured providers and list all open pull requests.
This is a dry-run mode that does not perform any AI review or post any comments.`,
	}
}

// Execute discovers repos and lists open PRs.
func (c *DiscoverController) Execute(cmd *cobra.Command, _ []string) {
	ctx := context.Background()

	configPath, _ := cmd.Flags().GetString("config")
	verbose, _ := cmd.Flags().GetBool("verbose")

	if verbose {
		logger.SetLevel(logger.DebugLevel)
	}

	cfgPath := configPath
	if cfgPath == "" {
		var err error
		cfgPath, err = configHelpers.FindConfigFile("code-guru")
		if err != nil {
			logger.Errorf("no config file found: %v", err)
			return
		}
	}

	settings, err := entities.NewSettings(cfgPath)
	if err != nil {
		logger.Errorf("failed to load config: %v", err)
		return
	}

	results, err := c.discoverCommand.Execute(ctx, settings)
	if err != nil {
		logger.Errorf("discover failed: %v", err)
		return
	}

	totalPRs := 0
	for _, result := range results {
		for _, repoResult := range result.Repositories {
			_, _ = fmt.Fprintf(os.Stdout, "\n%s/%s (%d open PRs):\n",
				repoResult.Repository.Organization,
				repoResult.Repository.Name,
				len(repoResult.PullRequests),
			)
			for _, pr := range repoResult.PullRequests {
				_, _ = fmt.Fprintf(os.Stdout, "  #%d %s (%s -> %s) by %s\n",
					pr.ID, pr.Title, pr.SourceBranch, pr.TargetBranch, pr.Author,
				)
				totalPRs++
			}
		}
	}

	_, _ = fmt.Fprintf(os.Stdout, "\nTotal: %d open PRs found\n", totalPRs)
}
