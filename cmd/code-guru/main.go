package main

import (
	"os"
	"slices"

	logger "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"

	"github.com/rios0rios0/codeguru/internal"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/domain/repositories"
	"github.com/rios0rios0/codeguru/internal/infrastructure/controllers"
)

// version is set at build time via -ldflags.
// During development, it defaults to "dev".
//

var version = "dev"

// runUpdateCheck queries the self-updater for a newer version, skipping local dev
// builds and the commands checksForUpdates leaves out.
func runUpdateCheck(command *cobra.Command, selfUpdater repositories.SelfUpdaterRepository) {
	if version == "dev" || !checksForUpdates(command) {
		return
	}
	selfUpdater.CheckForUpdates()
}

// checksForUpdates reports whether running command also checks for a newer
// release. The self-update and version subcommands skip it, to avoid noisy and
// meaningless checks. So does `health`, the container's HEALTHCHECK probe,
// which runs every 30 seconds where nobody reads its output, and so does
// shell completion: `completion` runs from a shell's startup file every time a
// shell starts, and cobra's hidden `__complete` on every TAB press. Both exit at
// once, so a lookup started there would never be read and would only use up the
// day's update check. `completion bash` is named `bash`, so a command is judged
// by its ancestor directly under the root.
func checksForUpdates(command *cobra.Command) bool {
	for command.HasParent() && command.Parent().HasParent() {
		command = command.Parent()
	}
	return !slices.Contains([]string{
		"self-update", "version", "health", "completion", cobra.ShellCompRequestCmd,
	}, command.Name())
}

func buildRootCommand(
	reviewController *controllers.ReviewController,
	selfUpdater repositories.SelfUpdaterRepository,
) *cobra.Command {
	//nolint:exhaustruct // minimal Command initialization with required fields only
	cmd := &cobra.Command{
		Use:     "code-guru [pr-url]",
		Short:   "AI-powered code review tool",
		Version: version,
		Long: `Code Guru automatically reviews pull requests using AI (OpenAI or Claude Code).
It analyzes code diffs against configurable review rules and posts
comments directly on the pull request.

Supports GitHub and Azure DevOps as Git hosting providers.

Usage modes:
  code-guru <pr-url>       Review a single PR by URL
  code-guru review-all     Review all open PRs across configured providers
  code-guru discover       List open PRs without reviewing them`,
		Args: cobra.MaximumNArgs(1),
		PersistentPreRun: func(command *cobra.Command, _ []string) {
			runUpdateCheck(command, selfUpdater)
		},
		RunE: func(command *cobra.Command, args []string) error {
			if len(args) == 0 {
				return command.Help()
			}
			reviewController.Execute(command, args)
			return nil
		},
	}

	cmd.PersistentFlags().StringP("config", "c", "", "path to config file")
	cmd.PersistentFlags().String("backend", "", "AI backend: openai, claude, or anthropic")
	cmd.PersistentFlags().String("rules-path", "", "path to rules directory")
	cmd.PersistentFlags().Bool("dry-run", false, "show review without posting comments")
	cmd.PersistentFlags().BoolP("verbose", "v", false, "enable verbose output")

	return cmd
}

func addSubcommands(rootCmd *cobra.Command, appContext *internal.AppInternal) {
	for _, controller := range appContext.GetControllers() {
		bind := controller.GetBind()
		ctrl := controller
		//nolint:exhaustruct // minimal Command initialization with required fields only
		subCmd := &cobra.Command{
			Use:   bind.Use,
			Short: bind.Short,
			Long:  bind.Long,
			Run: func(command *cobra.Command, arguments []string) {
				ctrl.Execute(command, arguments)
			},
		}
		if binder, ok := ctrl.(entities.FlagBinder); ok {
			binder.BindFlags(subCmd)
		}
		rootCmd.AddCommand(subCmd)
	}
}

func main() {
	//nolint:exhaustruct // minimal TextFormatter initialization with required fields only
	logger.SetFormatter(&logger.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	})
	if os.Getenv("DEBUG") == "true" {
		logger.SetLevel(logger.DebugLevel)
	}

	reviewController := injectReviewController()
	selfUpdater := injectSelfUpdater()
	cobraRoot := buildRootCommand(reviewController, selfUpdater)

	appContext := injectAppContext()
	addSubcommands(cobraRoot, appContext)

	if err := cobraRoot.Execute(); err != nil {
		logger.Fatalf("error executing 'code-guru': %s", err)
	}
}
