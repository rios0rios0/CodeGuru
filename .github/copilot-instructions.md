# Copilot Instructions

## Project Overview

Code Guru is an AI-powered CLI tool written in Go that automatically reviews pull requests.
It supports GitHub and Azure DevOps as Git hosting providers, and Anthropic Messages API, Claude Code CLI, or OpenAI Chat Completions API as AI backends.
Review rules are loaded from configurable Markdown files with optional YAML frontmatter for file-glob filtering.

## Architecture

The project follows **Clean Architecture** with strict layer separation:

- **`cmd/code-guru/`** — Application entry point. Builds the DI container, wires Cobra commands, and starts the CLI.
- **`internal/`** — Internal application wiring (`app.go`, `container.go`) that aggregates controllers.
- **`internal/domain/`** — Core business logic. Contains entity definitions, repository interfaces, and command implementations. This layer has no infrastructure dependencies.
  - `entities/` — Domain models (`FileDiff`, `ReviewComment`, `ReviewResult` (with `ThreadResolutions`), `ReviewRequest`, `ReviewThread` (with gitforge `ThreadID`/`RootCommentID`), `ThreadResolution`, `Rule`, `Settings`, `AuthToken`, `AppVersion`, `Controller`/`FlagBinder` interfaces).
  - `repositories/` — Abstract interfaces (`AIReviewerRepository`, `RulesRepository`, `TrivialDetector`/`TrivialDetectorRegistry`, `PullRequestMetadataRepository`, `TokenRepository`, `SelfUpdaterRepository`).
  - `commands/` — Use-case implementations (`ReviewCommand` — with `review_batches.go` for the context-window batching fallback —, `ReviewAllCommand`, `DiscoverCommand`, `AuthCommand`, `SelfUpdateCommand`, `VersionCommand`).
- **`internal/infrastructure/`** — Concrete implementations of domain interfaces.
  - `repositories/` — AI backend implementations (`anthropic/`, `claude/`, `openai/`), rule loading (`rules/`), trivial PR detectors (`trivial/`), PR metadata fetchers (`prmetadata/`: description + commit count via the GitHub / Azure DevOps REST APIs, dispatched by provider name), OAuth token storage (`auth/`), self-updater (`selfupdate/`). A `container.go` at this level provides `AIReviewerFactory` and `RulesRepositoryFactory` for settings-driven backend selection. The factory wraps the chosen backend in a `RetryingAIReviewer` decorator (`retrying_ai_reviewer.go`) that re-samples on a non-JSON / unparseable or transient-error response up to `ai.max_attempts` times (reinforcing the JSON-only instruction via `ReviewRequest.Attempt`), so a recoverable blip does not fail the review or surface the raw model output on the PR. A context-window overflow (`support.ErrContextWindowExceeded`, raised by every backend when the diff is too large for the model), a content-safety refusal (`support.ErrContentSafetyRefusal`, raised when the model's safety classifiers decline the content — Anthropic `stop_reason: "refusal"`, OpenAI `content_filter`), and an argument-limit refusal (`support.ErrArgumentListTooLong`, raised when the OS refuses to start a subprocess backend because the assembled prompt exceeds its per-argument limit) are all deterministic and returned immediately without retry. The command layer then answers each one specifically: an overflow triggers the batched-review fallback (see "Batched reviews" below) and only falls back to the "PR too large" annotation — the change's scale + split-the-PR guidance — when batching is disabled or every batch failed; a refusal posts the "content-safety declined" annotation with the policy category + request-a-human/switch-model guidance; an argument-limit refusal posts the "reviewer's own configuration exceeded an OS limit" annotation, which states the diff was never read and points an operator at `rules.categories`. None of the three surfaces the generic failure notice. The Anthropic backend can re-issue the review once against `ai.anthropic.refusal_fallback_model` when the primary model refuses.
  - `controllers/` — Cobra CLI controllers (review, review-all, discover, auth, serve, health, self-update, version) that bridge CLI input to domain commands.
  - `controllers/webhooks/` — HTTP webhook dispatcher (`dispatcher.go`) with auth (`auth.go`: HMAC-SHA256 + Basic Auth), per-vendor handlers (`github.go`, `azuredevops.go`), webhook dedup (`dedup_cache.go`: per-pod TTL cache; `dedup_lease.go`: K8s Lease cross-pod dedup; `dedup_lease_client.go`: the `coordination.k8s.io/v1` REST adapter the Lease backend runs on — plain `net/http`, in-cluster CA/token wiring, `MicroTime` wire format, `Status`-`reason` error classification, and no Kubernetes client library), CIDR allowlist (`source_ip.go`), ADO skinny-payload hydration (`azuredevops_hydrator.go`), GitHub App installation token exchange (`installation_token_exchange.go`: RS256 JWT, `sync.Map` cache), and a bounded async worker pool (`worker.go`).
- **`internal/support/`** — Shared utility functions: URL parsing, diff splitting, file classification, prompt building, response parsing, conversation assembly (prior bot review threads for re-review context), bot-identity recognition for the re-review walk (`IsBotAuthor` honours configured `bot_identities` plus `DetectBotAuthors` self-detection from the bot's own PR-wide annotations, so it works when the deployment posts under a service account), review markers and settings-aware mention detection (`HasMention` scans the built-in `@code-guru` token plus the mention tokens derived from `bot_identities`; `ExtractMentionedIdentityIDs` + `EqualIdentityID` recognise Azure DevOps' `@<identity-guid>` autocomplete markup, which the ADO webhook handler compares against the identity its own PAT authenticates as), verdict-to-native-review mapping, byte-bounded text truncation.
- **`test/domain/doubles/`** — Test doubles (stubs) for domain repository interfaces.
- **`test/infrastructure/doubles/`** — Test doubles for infrastructure-only types (e.g., webhook `Submitter`, `GitHubTokenizer`, `WebhookDedup`).
- **`configs/`** — Example YAML configuration files.

## Dependency Injection

The project uses **Uber DIG** (`go.uber.org/dig`) for dependency injection. Each package exposes a `RegisterProviders(container *dig.Container) error` function in a `container.go` file. Providers are registered bottom-up: repositories → entities → commands → controllers → app.

## CLI Framework

The CLI is built with **Cobra** (`github.com/spf13/cobra`). Controllers implement the `entities.Controller` interface:

```go
type Controller interface {
    GetBind() ControllerBind
    Execute(command *cobra.Command, arguments []string)
}
```

Controllers are automatically registered as subcommands via the DI container.

## Go Conventions

- **Go version**: 1.27+ (see `go.mod`).
- **Formatting**: Use `gofmt`; tabs for indentation (see `.editorconfig`).
- **Imports**: Group into standard library, external dependencies, and internal packages (separated by blank lines). Alias the `logrus` logger as `logger` and `gitforge` packages with `forge` prefixes.
- **Error handling**: Always return errors explicitly. Wrap errors with `fmt.Errorf("context: %w", err)` for context propagation.
- **Naming**: PascalCase for exported identifiers. Use descriptive suffixes: `Repository`, `Command`, `Controller`, `Config`, `Factory`. Private helpers use camelCase.
- **Comments**: Every exported type and function must have a GoDoc comment starting with the identifier name. Keep comments concise and descriptive of intent.
- **Nolint directives**: Use `//nolint:exhaustruct` when intentionally initializing structs with only required fields. Always include a justification comment.

## Testing

- **Build tags**: unit tests carry NO build tag — a constraint would hide them from `go test ./...`, IDE runs, and the linter. Run tests with `go test ./...`. Only an integration suite needing real infrastructure would be gated, with `//go:build integration`.
- **Package naming**: Test files use the `_test` suffix on the package name (e.g., `package support_test`).
- **Framework**: Use `github.com/stretchr/testify/assert` and `github.com/stretchr/testify/require` for assertions.
- **Structure**: Follow Given-When-Then with `// given`, `// when`, `// then` comments. Use table-driven tests with `t.Run()` subtests. Call `t.Parallel()` at the top of test functions.
- **Test doubles**: Place stubs in `test/domain/doubles/repositories/` for domain-contract stubs and `test/infrastructure/doubles/repositories/` for stubs that double infrastructure-only types. Name them with the `Stub` prefix (e.g., `StubAIReviewerRepository`). Stubs store the last request and return canned responses. Keep the layer the stub references on the same side of the import boundary.

## Logging

Use `github.com/sirupsen/logrus` aliased as `logger`. Use structured log levels: `logger.Infof`, `logger.Errorf`, `logger.Debugf`, `logger.Warnf`.

## Configuration

Settings are loaded from YAML files discovered automatically by `FindConfigFile`. Searched locations (in order): `.`, `.config`, `configs`, `~`, `~/.config`. Accepted filenames: `.code-guru.yaml`, `.code-guru.yml`, `code-guru.yaml`, `code-guru.yml`. Pass an explicit path with `-c/--config` to override discovery.

Token fields support three resolution strategies in order: **environment variable** (`${VAR_NAME}`), **file path** (contents read if resolved string is a valid file), and **inline** (literal string).

Key config sections:

- `providers[]` — list of Git hosting providers (`type: github|azuredevops`, `token`, `organizations[]`).
- `ai.backend` — required; `openai`, `claude`, or `anthropic`.
- `ai.openai` — `api_key`, `model` (e.g. `gpt-4o`). `api_key` is required when backend is `openai`.
- `ai.anthropic` — `api_key`, `model` (default `claude-sonnet-4-20250514`), `context_1m` (tri-state, default on: sends the `context-1m-2025-08-07` beta for the 1M-token window; env `CODE_GURU_ANTHROPIC_CONTEXT_1M`, resolve via `AnthropicConfig.Context1MEnabled()`), `refusal_fallback_model` (env `CODE_GURU_ANTHROPIC_REFUSAL_FALLBACK_MODEL`, default empty/off: the model re-issued against on a content-safety `stop_reason: "refusal"`). `api_key` is required when backend is `anthropic`.
- `ai.claude` — `binary_path` (default `claude`), `model` (default `sonnet`), `max_turns` (default `1`).
- `ai.max_attempts` — AI retry budget per review (env `CODE_GURU_AI_MAX_ATTEMPTS`, default `3`; `1` disables retries). Resolve via `AIConfig.ReviewAttempts()`.
- `ai.project_guidelines` — tri-state; when enabled (default), the reviewed repository's own root `CLAUDE.md` is fetched via the provider's file-access API and forwarded to the LLM as project-specific review context (env `CODE_GURU_AI_PROJECT_GUIDELINES`). Resolve via `AIConfig.ProjectGuidelinesEnabled()`; never dereference the pointer directly.
- `ai.pr_metadata` — tri-state; when enabled (default), the PR's description and commit count are fetched from the provider's REST API and forwarded to the LLM as intent context (env `CODE_GURU_AI_PR_METADATA`). Resolve via `AIConfig.PullRequestMetadataEnabled()`; never dereference the pointer directly.
- `ai.batch_large_reviews` — tri-state; when enabled (default), a pull request whose prompt exceeds the model's context window is reviewed in batches instead of being skipped (env `CODE_GURU_AI_BATCH_LARGE_REVIEWS`). Resolve via `AIConfig.BatchLargeReviewsEnabled()`; never dereference the pointer directly.
- `ai.max_review_batches` — cap on how many batches one batched review may consume (env `CODE_GURU_AI_MAX_REVIEW_BATCHES`, default `20`). Resolve via `AIConfig.ReviewBatches()`.
- `rules.path` — directory containing Markdown rule files (supports `${VAR}` expansion).
- `rules.categories` — optional allow-list of rule categories to load; empty means load all.
- `server.port` — webhook server port (default `8080`).
- `server.webhook_secret` — HMAC secret for verifying webhook payloads.
- `github_app.app_id` — GitHub App ID for webhook authentication.
- `github_app.private_key` — GitHub App private key.
- `bot_identities` — comma-separated account identities code-guru posts under (env `CODE_GURU_BOT_IDENTITIES`). Drives two things: re-reviews recognise prior bot threads under a service account, AND each entry becomes an extra `@`-mention that triggers a re-review (`code-guru[bot]` → `@code-guru`, `svc-x@corp.example` → `@svc-x`, an Azure DevOps identity GUID → the `@<guid>` markup the ADO comment box emits). On Azure DevOps that GUID form is also matched automatically against the bot's own identity (resolved once per org from `_apis/connectionData`), so an autocompleted mention needs no configuration. The built-in `code-guru` name shapes — `code-guru[bot]` (GitHub App), `code-guru@<tenant>` (Azure DevOps), and bare `code-guru` — plus self-detected identities are always recognised, so this is only needed for service accounts that don't follow those shapes. Because entries are live triggers, list only accounts this bot posts under.

Validate required fields in `validateSettings`.

## Rules

Rules are Markdown files stored in the directory specified by `rules.path`. Each file becomes one rule, using its filename (without `.md`) as both its name and category.

**Frontmatter**: A rule file may start with a YAML frontmatter block delimited by `---`. The only supported frontmatter key is `paths`, a list of glob patterns restricting the rule to specific changed files:

```markdown
---
paths:
  - "**/*.go"
---
# Go Conventions
...
```

**Category filtering**: `FilesystemRulesRepository.LoadForLanguages` always includes rules in the following *universal* categories regardless of detected languages: `architecture`, `ci-cd`, `code-style`, `design-patterns`, `documentation`, `git-flow`, `security`, `testing`. Language-specific rules are included when their category matches a detected language, or when their `paths` globs match the changed files.

**Project guidelines**: On top of the configured rules, the review command loads the reviewed repository's own root `CLAUDE.md` (`internal/domain/commands/project_guidelines.go`) and forwards it on `ReviewRequest.ProjectGuidelines`; `support.BuildUserPromptFor` renders it into the user prompt as a fenced, escape-proofed documentation block. The fetch is skipped when the PR itself modifies `CLAUDE.md`, is best-effort (missing file or provider error never fails the review), and bounds content to `ai.max_guidelines_bytes` (default 1 MiB, `AIConfig.GuidelinesBytes()`).

**Pull request metadata**: The review command also loads the PR's description and commit count (`internal/domain/commands/pull_request_metadata.go`, via the `prmetadata` fetcher registry) and forwards them on `ReviewRequest.Metadata`; the prompt builder renders them — together with the title and branch names already in the PR header — as an intent-context section instructing the model to verify the diff against the stated intent and flag scope creep. Best-effort (an unsupported provider or API error never fails the review); the description is bounded to `ai.max_pr_description_bytes` (default 64 KiB, `AIConfig.PRDescriptionBytes()`) and escape-proofed like every other untrusted block.

**Batched reviews (context-window fallback)**: When the assembled prompt exceeds the model's context window (`support.ErrContextWindowExceeded`) and `ai.batch_large_reviews` is on (default), `internal/domain/commands/review_batches.go` posts a "reviewing this PR in batches" notice on the pull request and then splits the files into batches that fit, reviewing them sequentially and merging the results (union of comments, most severe verdict, joined summaries, thread resolutions re-keyed from batch-local to run-global ids). The batch size is derived from the token figures in the backend's own error (`support.ParseContextWindowOverage`) and halved on any batch that still overflows. Each batch's request carries `ReviewRequest.Batch`, which makes the prompt state that only part of the change is visible — without it the model reports the files it cannot see as missing tests, unused symbols, or an incomplete change. Files that never reach the model (one file larger than the window, a failed batch, or the `ai.max_review_batches` cap) are named in the merged summary, and such a review never returns `approve`. If not one batch succeeds, the command falls back to the historical "PR too large" annotation.

## CLI Usage

Available subcommands:

| Command        | Description                                              |
|----------------|----------------------------------------------------------|
| `review <url>` | Review a single PR by URL (GitHub or Azure DevOps)       |
| `review-all`   | Batch-review all open PRs across configured providers    |
| `discover`     | Discover repos and list open PRs without posting reviews |
| `serve`        | Start webhook server for automatic PR review             |
| `health`       | Probe a running `serve` listener (Docker healthcheck client) |
| `self-update`  | Update the CLI binary to the latest version              |
| `version`      | Print the current CLI version                            |

The `auth` controller and its filesystem token store exist in the tree but are **not** DI-registered (login is a TODO), so `auth` is not an available subcommand.

Common flags (all commands):

| Flag              | Description                                     |
|-------------------|-------------------------------------------------|
| `-c, --config`    | Path to config file (default: auto-discover)    |
| `-v, --verbose`   | Enable debug logging                            |
| `--dry-run`       | Perform review without posting comments (`review-all` only) |

## Build and CI

- **Build**: `go build -o bin/code-guru ./cmd/code-guru/`
- **Test**: `go test ./...`
- **CI**: GitHub Actions workflow in `.github/workflows/default.yaml` using reusable pipelines from `rios0rios0/pipelines`. Includes golangci-lint, CodeQL SAST, and SonarCloud quality gates.
- **Makefile**: Includes external makefiles from `rios0rios0/pipelines` for common and Go-specific targets.

## Dependencies

Only add new dependencies when strictly necessary. Prefer the standard library. Current key dependencies:

- `github.com/rios0rios0/cliforge` — CLI utilities and self-update support
- `github.com/rios0rios0/gitforge/v4` — Multi-provider Git abstraction (consumed as a tagged release; no local `replace` directive)
- `github.com/rios0rios0/langforge` — Language classification by file extension
- `github.com/rios0rios0/testkit` — Test builder base utilities
- `github.com/sashabaranov/go-openai` — OpenAI API client
- `github.com/sirupsen/logrus` — Structured logging
- `github.com/spf13/cobra` — CLI framework
- `github.com/stretchr/testify` — Testing assertions
- `go.uber.org/dig` — Dependency injection
- `gopkg.in/yaml.v3` — YAML parsing

**Deliberately absent: Kubernetes client libraries.** `k8s.io/client-go` (+ `api`/`apimachinery`) was removed — it pulled ~40 transitive modules for four verbs on one resource, its release train has to move in lockstep (one out-of-step `kube-openapi` bump stopped the build compiling), and `client-go` master carries a `v0.0.0-` pseudo-version that sorts below the "fixed in" version of two 2021 advisories, so `govulncheck` flagged them against current code forever. The cross-pod dedup Lease backend speaks the REST API directly in `dedup_lease_client.go`. Do not reintroduce it.

## Contributing

See [CONTRIBUTING.md](../CONTRIBUTING.md) for the development workflow and the [Development Guide](https://github.com/rios0rios0/guide/wiki) for coding standards.

<!-- chlog:start -->
## Changelog (chlog) — MANDATORY

If the repository you are working in uses chlog (a `.chlog.yaml` or `.chlog.yml`
config file, or a `.changes/` directory, exists at the project root), the
following is binding and ALWAYS applies: whenever you make ANY change, you MUST
create a changelog fragment as part of the same change — automatically, without
being asked, before committing.

- Do NOT edit CHANGELOG.md directly; it is generated from fragments.
- Create the fragment with:
  `chlog new --kind <Kind> --body '<past-tense description>'`
- Write an apostrophe inside the single-quoted body as `'\''`.
- Valid kinds: Added, Changed, Deprecated, Removed, Fixed, Security
- Choose the kind that best matches the change (e.g., new feature → Added,
  bug fix → Fixed, behavior change → Changed, removal → Removed, security fix → Security).
- If the change is backward-INCOMPATIBLE with the public API (a breaking
  change), you MUST add the `--breaking` flag:
  `chlog new --kind <Kind> --breaking --body '<past-tense description>'`.
  This is the ONLY thing that triggers a major version bump — the kind alone
  never does (per SemVer, major = incompatible change). When unsure whether a
  change breaks compatibility, ask the user instead of guessing.
- Fragments are YAML files in `.changes/unreleased/`; stage them with your commit.
- `chlog check` fails the build when a fragment is missing — never skip it.
<!-- chlog:end -->
