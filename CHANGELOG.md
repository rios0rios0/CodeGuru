# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

This file is not edited by hand. Every change writes its own fragment under
`.changes/unreleased/` with [chlog](https://github.com/luizjhonata/chlog), and a release compiles
the pending fragments into a version section here — so two branches each adding an entry no
longer touch the same lines, and a rebase that used to conflict on this file now conflicts on
nothing.

When a new release is proposed:

1. Create a new branch `bump/x.x.x` (this isn't a long-lived branch!!!);
2. The fragments pending under `.changes/unreleased/` are compiled into a version section by `chlog batch auto && chlog merge` (AutoBump does this for you — it reads the fragments directly);
3. Open a Pull Request with the bump version changes targeting the `main` branch;
4. When the Pull Request is merged, a new Git tag must be created using [GitHub environment](https://github.com/rios0rios0/code-guru/tags).

Releases to productive environments should run from a tagged version.
Exceptions are acceptable depending on the circumstances (critical bug fixes that can be cherry-picked, etc.).

## [Unreleased]

## [1.19.3] - 2026-10-07

### Changed

- changed the gitforge dependency from `github.com/rios0rios0/gitforge` `v1.0.1-0.20260723193418-4150608363f0` to `github.com/rios0rios0/gitforge/v4` `v4.2.10`: the unsuffixed module path never resolved past `v1.0.0`, so gitforge could only be pinned to a pseudo-version and dependency updates never reached it
- changed the Go module dependencies to their latest versions

### Fixed

- fixed shell completion and the container health probe checking for a newer release: `completion`, which a shell runs from its startup file, the `__complete` requests behind every TAB press and the `health` command the image's `HEALTHCHECK` runs every 30 seconds each started a lookup nobody could see, spending the day's update check before a review ever ran

### Security

- escaped line breaks in the webhook data the server logs, so a value from a delivery can no longer start a forged log line, and checked the Azure DevOps hydration URL against an anchored pattern that also refuses user info and ports, called directly where the request is built

## [1.19.2] - 2026-09-30

### Changed

- changed the Go module dependencies to their latest versions

## [1.19.1] - 2026-09-14

### Changed

- changed the Go module dependencies to their latest versions
- corrected the `CLAUDE.md` feature catalog to state the 1 MiB project-guidelines and 64 KiB PR-description bounds, matching the code and the rest of the document
- replaced `k8s.io/client-go` with a direct `coordination.k8s.io/v1` REST adapter over `net/http` for the cross-pod webhook dedup Lease backend, removing ~40 transitive modules, the recurring `structured-merge-diff` version skew, and two permanent `govulncheck` false positives; the RBAC the bot needs is unchanged

### Fixed

- pinned k8s.io/kube-openapi to the version compatible with the pinned k8s.io/apimachinery and k8s.io/client-go release, resolving a structured-merge-diff v6/v7 type mismatch that broke the build

## [1.19.0] - 2026-09-09

### Added

- added zero-configuration recognition of an Azure DevOps `@`-autocompleted mention: a comment carrying the comment box's `@<identity-guid>` markup is now matched against the identity the bot's own PAT authenticates as, resolved once per organization from `_apis/connectionData`, so picking the bot out of the autocomplete requests a re-review without listing its identity GUID in `bot_identities`

### Changed

- ignored the `.codeql-db/` directory that `make sast` builds in place, so a CodeQL run no longer leaves an untracked database in the working tree
- surfaced the Azure DevOps webhook's one remaining silent drop at `Info` — a comment carrying `@<identity-guid>` markup on an allow-listed organization whose own bot identity could not be resolved, the state in which a mention of the bot itself is dropped unseen; a GUID that resolves to another account and an off-allowlist delivery both stay at `Debug`, so one human @-mentioning another never reaches the operator log

### Removed

- removed the `unit` build tag from every test file so `go test ./...`, IDE runs and the linter all see the unit suite (`make test` still passes `-tags test,unit`), and fixed the lint findings the newly-visible files surfaced — `t.Parallel()` on every subtest, `require` for error assertions, and a checked type assertion in the entity builders

## [1.18.8] - 2026-09-08

### Changed

- changed both `chlog new` examples in the AI-assistant instruction block of `CLAUDE.md` and `.github/copilot-instructions.md` to `--body '<past-tense description>'`: changelog bodies here are written in simple past tense, and the body is single-quoted because it carries backticks that a double-quoted shell argument would command-substitute, and added the line telling the reader to write an apostrophe inside the single-quoted body as `'\''`, since bodies here carry possessives, and switched the 5 other hand-written `chlog new` examples in `CLAUDE.md`, `CONTRIBUTING.md`, `.github/pull_request_template.md`, `.github/pull_request_template/default.md`, and `.github/skills/code-review/SKILL.md` to the same single-quoted body argument

## [1.18.7] - 2026-09-07

### Changed

- changed the Docker base image `debian` from `12-slim` to `13-slim`
- changed the Go module dependencies to their latest versions
- extracted the scale sentence shared by the "review failed" and "reviewing in batches" notices into one `reviewFailureContext` helper, so the two notices quantify a too-large change identically
- pinned `k8s.io/kube-openapi` to the revision `k8s.io/apimachinery` v0.37.0 builds against, since newer revisions moved to `sigs.k8s.io/structured-merge-diff/v7` and no longer compiled

### Fixed

- declared test files as test sources for SonarCloud Automatic Analysis so duplicated test setup no longer fails the quality gate

### Security

- restricted the `curl` downloads in the delivery Dockerfile (Claude Code installer) and in the Azure Pipelines and GitHub Actions examples to HTTPS-only redirects with `--proto "=https" --proto-redir "=https"`, so a redirect can no longer downgrade the download to plain HTTP (Sonar S6506)

## [1.18.6] - 2026-09-04

### Changed

- changed the Go module dependencies to their latest versions

## [1.18.5] - 2026-09-03

### Changed

- changed the Go module dependencies to their latest versions

## [1.18.4] - 2026-09-02

### Changed

- changed the Docker base image `golang` from `1.27.0-alpine` to `1.27.1-alpine`
- changed the Go version to `1.27.1` and updated all module dependencies

## [1.18.3] - 2026-09-01

### Changed

- changed the Go module dependencies to their latest versions
- refreshed `CLAUDE.md` to reference the renamed Claude workflows `claude-review.yaml` and `claude-mention.yaml` instead of the stale `claude-code-review.yaml` and `claude.yaml`

## [1.18.2] - 2026-08-29

### Changed

- changed the Go module dependencies to their latest versions

## [1.18.1] - 2026-08-28

### Changed

- changed the Claude workflows to call the reusable workflows in `rios0rios0/pipelines` instead of `rios0rios0/.github`, which is where every other reusable workflow and composite action already lives, and renamed them to `claude-review.yaml` and `claude-mention.yaml`, matching the `reusable-claude-review.yaml` / `reusable-claude-mention.yaml` definitions they call
- changed the Go module dependencies to their latest versions

### Fixed

- restored the `.changes/unreleased/` directory with a `.gitkeep`, so the release tooling keeps recognising this project as [chlog](https://github.com/luizjhonata/chlog)-based after a release consumes the last fragment. Git tracks files rather than directories, so the bump commit that removed the final fragment removed the directory too, and the next run read the empty `[Unreleased]` section as "nothing to release"
- restored the `id-token: write` permission on both Claude workflow callers. Without it the caller grants less than the reusable workflow declares, which GitHub rejects before the job starts -- runs ended in `startup_failure`. The action needs the scope because `setupGitHubToken()` exchanges a GitHub OIDC token for the GitHub App token it posts with, unless a `github_token` is passed explicitly.

### Removed

- removed the unused `id-token: write` permission from the Claude workflow callers, and changed `claude-review.yaml`'s display name to `Claude Review` so it matches its file name and its `Claude Mention` sibling. `anthropics/claude-code-action` needs `id-token: write` only for workload identity federation or the Bedrock / Vertex / Foundry OIDC paths; these authenticate with `claude_code_oauth_token`, so the scope allowed minting OIDC tokens for any audience without ever being used.

## [1.18.0] - 2026-08-26

### Added

- added a tailored `code-review` skill under `.github/skills/` so GitHub Copilot reviews changes against the [rios0rios0/guide](https://github.com/rios0rios0/guide/wiki) standards and this repository's own load-bearing invariants

### Changed

- changed the changelog to [chlog](https://github.com/luizjhonata/chlog) fragments: a change now writes its own YAML file under `.changes/unreleased/` through `chlog new --kind <Kind> --body "..."`, and `CHANGELOG.md` is GENERATED from them at release time by `chlog batch auto && chlog merge`. That is the one thing a single shared file cannot do — two branches each adding an entry no longer touch the same lines, so a rebase that used to conflict on `CHANGELOG.md` now conflicts on nothing. The `[Unreleased]` section was empty, so nothing had to be carried across. AutoBump already reads the fragments directly, so the release flow is unchanged.
- changed the Go module dependencies to their latest versions

### Fixed

- fixed the `main` pipeline, which every repository's `sast:gitleaks` job had been failing since the code-review skill landed: the skill's own security bullet listed credential prefixes verbatim to warn against writing them, and the scanner's second pass matches those prefixes on their own, so the warning tripped the rule it was describing. The bullet now names the vendors instead, and the commit that carried the original wording is allowlisted by fingerprint in `.gitleaksignore`, because the scan walks the whole history reachable from `HEAD` and no edit at the tip can clear a past commit. No credential was ever committed.

## [1.17.1] - 2026-08-25

### Changed

- changed the Go module dependencies to their latest versions

## [1.17.0] - 2026-08-24

### Added

- added `cmd/code-guru/dockerfile_test.go`, a unit test asserting the delivery `Dockerfile` builder image is never older than the `go` directive in `go.mod`, so the drift that broke the Docker stage twice now fails in the test job instead of at the end of the pipeline

### Changed

- changed `AuthController` to depend on the new `commands.Auth` contract instead of the concrete command, matching every other controller
- changed struct literals and `errors.As` calls to the Go 1.27 forms required by the `modernize` linter
- changed the Go module dependencies to their latest versions
- changed the Go version to `1.27.0` and updated all module dependencies

### Fixed

- fixed `make test` and `make sast` leaving generated reports (`coverage.txt`, `coverage.xml`, `cobertura.xml`, `junit.xml`) as untracked files by adding them to `.gitignore`
- fixed the `README.md`, `CONTRIBUTING.md`, and `.github/copilot-instructions.md` Go version references, which still pointed at Go `1.26`
- fixed the Docker delivery stage failing with `go.mod requires go >= 1.27.0 (running go 1.26.6; GOTOOLCHAIN=local)`: the builder image stayed on `golang:1.26.6-alpine` when `go.mod` moved to Go `1.27.0`, so the build aborted at `go mod download` and no container image was published

## [1.16.2] - 2026-08-17

### Changed

- changed the Go module dependencies to their latest versions

### Fixed

- fixed the `README.md` Docker instructions, which pointed `docker build` at the repository root (the `Dockerfile` moved to `.ci/stages/40-delivery/app.Dockerfile`) and described a `gcr.io/distroless/static-debian12:nonroot` runtime the image no longer uses
- fixed the Docker delivery stage failing with `go.mod requires go >= 1.26.6 (running go 1.26.5; GOTOOLCHAIN=local)`: the builder image stayed on `golang:1.26.5-alpine` when `go.mod` moved to Go `1.26.6`, and because the official `golang` images pin `GOTOOLCHAIN=local` the build aborted at `go mod download`, so no container image was published for `1.16.0` or `1.16.1`

## [1.16.1] - 2026-08-16

### Changed

- changed the Go module dependencies to their latest versions

## [1.16.0] - 2026-08-15

### Added

- added `README.md` documentation of the language rule categories the classifier can produce, so an operator can tell which filename a language-specific rule file needs
- added Dart to the languages the reviewer recognises: a pull request touching `.dart` files now selects the `dart` rule category and labels those hunks as Dart in the prompt, where before they classified as unknown and contributed no language rules. Operators who keep a `dart.md` in their `rules.path` get it enforced automatically; those who do not are unaffected, since an unmatched category simply loads nothing

### Changed

- changed `langforge` from `v0.6.11` to `v1.0.0`. The major release removed the nine per-ecosystem `Provider` structs (replaced by `repositories.CompositeProvider`) and the `javagradle`/`javamaven` `RuntimeManager` types (replaced by a shared `java.RuntimeManager`); Code Guru names none of those symbols, consuming only `pkg/domain/entities` for extension-based classification, so the breaking changes pass it by
- changed the Go version to `1.26.6` and updated all module dependencies

## [1.15.3] - 2026-08-13

### Changed

- changed the Go module dependencies to their latest versions

## [1.15.2] - 2026-08-12

### Changed

- changed the Go module dependencies to their latest versions

## [1.15.1] - 2026-08-11

### Changed

- changed the Go module dependencies to their latest versions

## [1.15.0] - 2026-08-07

### Added

- added a "Mentioning the bot" section to `README.md` documenting both trigger forms, where each works, and the Azure DevOps auto-complete caveat
- added handling of the GitHub `pull_request_review_comment` event, so a mention typed as a reply inside an inline review thread requests a re-review. GitHub routes only pull-request-wide comments through `issue_comment`, so those mentions were previously dropped entirely — Azure DevOps has always covered both. **The GitHub App must subscribe to `Pull request review comments`** or the new handler never receives a delivery
- added the bot's own account name as a re-review trigger, so mentioning the account users actually see on the pull request works alongside `@code-guru`. Every `bot_identities` entry is normalised into the mention a human would type: `code-guru[bot]` accepts `@code-guru`, `svc-codeguru@corp.example` accepts `@svc-codeguru`, and an Azure DevOps identity GUID accepts the `@<guid>` markup the Azure DevOps comment box substitutes for an auto-completed mention

### Changed

- changed `bot_identities` from a self-recognition-only list into a list that also drives mention triggers; entries are now live re-review triggers, so only accounts this bot posts under belong there (third-party automation accounts belong in `trivial.auto_merge_allowed_authors`)
- changed the Go module dependencies to their latest versions

### Fixed

- fixed a burst of GitHub inline mentions enqueuing one review per comment: a single review submission carrying the mention in several inline comments fires one webhook delivery each, which would have started that many concurrent reviews of the same pull request. The inline path now takes the duplicate-delivery gate under its own `gh-mention:` key, leaving the pull-request-wide path's always-goes-through behaviour untouched
- fixed a test that claimed to cover the unsupported-webhook-event branch while sending a supported event (`issue_comment`), passing only because the payload's action did not match

## [1.14.2] - 2026-08-04

### Changed

- changed the Go module dependencies to their latest versions
- refreshed `.github/copilot-instructions.md` to fix the CLI subcommands table, which listed the unwired `auth` stub as an available command and omitted the actual `health` probe command

## [1.14.1] - 2026-07-30

### Changed

- changed the Go module dependencies to their latest versions

## [1.14.0] - 2026-07-29

### Added

- added a distinct failure classification for a review the operating system refuses to start because the assembled review instructions are too long to pass as a command-line argument. It is no longer retried — the kernel refuses the byte-for-byte identical call every time, so the retry budget was spent for nothing — and the pull request now gets a notice explaining that the reviewer's own configuration is oversized, that the diff was never read, and that an operator can fix it by narrowing `rules.categories`. Previously this surfaced as the generic "the AI backend errored — this is usually transient, push a new commit", advice that could never help

### Fixed

- fixed reviews failing outright on pull requests that touch several languages at once. The Claude CLI backend passed the assembled review instructions — which embed every matching rule file — as a single command-line argument, and Linux caps one argument at 128 KiB no matter how much total argument space the system reports. A change touching, for example, a Go file and a Python file loads both language rule sets on top of the universal ones, which was enough to cross that cap: the operating system refused to start the backend, so the review died before the AI model was ever reached and all three attempts died identically. The review instructions are now handed over in a file (`--system-prompt-file`) instead, which removes the ceiling entirely. A deployment with no writable temporary directory falls back to the previous inline behaviour. Requires a Claude CLI version that supports the flag

## [1.13.1] - 2026-07-28

### Changed

- changed the Go module dependencies to their latest versions

## [1.13.0] - 2026-07-27

### Added

- added `trivial.delete_source_branch` (`CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH`, tri-state, default **on**) so a trivial PR that auto-merges also has its source branch deleted once the merge completes. The bot forwards `gitforge`'s new `WithDeleteSourceBranch` merge option — Azure DevOps removes the branch server-side on the completion call, GitHub deletes the head ref afterwards. It only takes effect when `trivial.auto_merge` fires (no merge, no branch to delete), and branch deletion is best-effort in the provider so a failure never fails the merge. Set it to `false` (or `CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH=false`) to keep the branch. Requires the `gitforge` version that ships the option.
- added batched reviews for pull requests that do not fit the AI model's context window. A change too large for a single prompt used to get no review at all — only a "this pull request is too large, split it up" notice, on exactly the pull requests that most need a reviewer. Code Guru now splits the change into batches that do fit, reviews them one after another, and merges the results into a single review with the union of the findings and the most severe verdict any batch reported. The pull request gets a "reviewing this PR in batches" notice up front explaining that the review is coming but will take several times longer, and each batch's prompt states that it holds only a slice of the change, so the model does not report the files it cannot see as missing tests, missing callers, or an incomplete change. Files that still cannot be read (a single file larger than the window) are named in the final summary, and a review that could not cover everything never approves the pull request. Controlled by `ai.batch_large_reviews` (`CODE_GURU_AI_BATCH_LARGE_REVIEWS`, tri-state, default **on**) and bounded by `ai.max_review_batches` (`CODE_GURU_AI_MAX_REVIEW_BATCHES`, default `20`); set the former to `false` to restore the previous give-up behaviour

### Changed

- changed the Go module dependencies to their latest versions
- refreshed `.github/copilot-instructions.md` to correct the project-guidelines and PR-description prompt budgets (now `ai.max_guidelines_bytes`, default 1 MiB, and `ai.max_pr_description_bytes`, default 64 KiB), which had drifted from the stale 32 KiB / 16 KiB values predating the 1.12.0 budget change

## [1.12.0] - 2026-07-23

### Added

- added operator-configurable prompt budgets for the two documents the reviewer loads alongside the diff: `ai.max_guidelines_bytes` (`CODE_GURU_AI_MAX_GUIDELINES_BYTES`) and `ai.max_pr_description_bytes` (`CODE_GURU_AI_MAX_PR_DESCRIPTION_BYTES`). Lower them when the configured backend has a small context window; leaving them unset keeps the shipped defaults

### Changed

- changed the Go module dependencies to their latest versions
- changed the project-guidelines budget from 32 KiB to 1 MiB (~256k tokens, about a quarter of a 1M-token context window) and the pull request description budget from 16 KiB to 64 KiB, so a large but entirely legitimate `CLAUDE.md` now reaches the model whole. The old 32 KiB ceiling was sized for a 200K-token window and silently truncated well-maintained guidelines files mid-document, leaving the model to judge the diff against half of the project's conventions — a worse failure than a long prompt, because nothing surfaced that the standard was incomplete. Truncation now also logs a warning naming the file size and the budget. **Deployments on a small-context-window model (for example a 128K-token backend, or Anthropic with `ai.anthropic.context_1m` disabled) should lower `ai.max_guidelines_bytes`**: the new default assumes a 1M-token window, and a pathologically large guidelines file could otherwise claim context the diff needs

### Fixed

- fixed reviews failing with no comments posted on the Claude CLI backend when the model reached for a tool instead of answering: the CLI was invoked with its full built-in toolset in scope, so the model would spend its turns on tool calls (most often after reading a reviewed repository's own guidelines, where a line such as "audit this with `<shell command>` if in doubt" reads as an instruction to execute rather than documentation to apply) and exit with `error_max_turns` before ever emitting the review, leaving only a generic "the AI backend errored" annotation on the pull request; the backend now passes an empty `--tools`, which removes every tool from the model's scope so a review is the one-shot text completion it was always meant to be. Restricting execution alone (`--disallowedTools`) does not fix this — the definitions stay in scope, the model still emits tool calls, and the turns are still consumed

## [1.11.0] - 2026-07-22

### Added

- added a dedicated "content-safety declined" failure notice: when the AI model's safety classifiers decline to review a change (common for security-related code — `stop_reason: "refusal"` on Anthropic, a `content_filter` finish reason on OpenAI), the PR annotation now names that cause, surfaces the provider's policy category when reported (e.g. cybersecurity-related content), reassures that it is a limitation of the reviewer's safety filters rather than a judgment that the change is malicious, and points at the real remedies (a human review; an operator fallback model or a different backend) — never "retry", since the same content is declined the same way; classified into a shared `ErrContentSafetyRefusal` class across backends
- added a dedicated "pull request too large" failure notice: when a review fails because the assembled prompt (diff plus rules, guidelines, metadata, and conversation) exceeds the AI model's context window, the PR annotation now names that specific cause, reports the change's scale (file count and total diff size), and gives correct next steps (split the PR into smaller ones, exclude generated/vendored/lock files) instead of the generic "usually transient — push a new commit" message, which was misleading for an over-large change (pushing more commits only grows the diff); the notice sets the review-once gate so a too-large PR is not re-reviewed and re-failed on every push; every backend (Anthropic, OpenAI, Claude CLI) now classifies its provider-specific "prompt too long" error into this shared class
- added an operator-configurable Anthropic refusal fallback model (`ai.anthropic.refusal_fallback_model`, `CODE_GURU_ANTHROPIC_REFUSAL_FALLBACK_MODEL`, default off): when the primary model declines a review on content-safety grounds, the backend re-issues the review once against the configured model, since safety-classifier coverage varies by model; a fallback that also refuses surfaces the original refusal
- added the Anthropic 1M-token context window: the Anthropic backend requests the `context-1m-2025-08-07` beta so pull requests up to ~5x larger fit in a single review pass before overflowing the default 200K window; controlled by the new tri-state `ai.anthropic.context_1m` setting (`CODE_GURU_ANTHROPIC_CONTEXT_1M`, default on) so operators whose account or model cannot use the beta can opt out (for prompts under 200K tokens the beta is a no-op, so small PRs are unaffected; very large prompts may incur Anthropic long-context pricing)

### Changed

- changed the Go module dependencies to their latest versions
- changed the retry decorator to also stop immediately on a content-safety refusal: a refusal on the same content is deterministic, so retrying only wasted the attempt budget
- changed the retry decorator to stop immediately on a context-window overflow instead of re-sending the byte-for-byte identical oversized prompt for every attempt: a prompt-too-long failure is deterministic, so retrying was guaranteed to fail the same way and only wasted the attempt budget (and, on paid backends, the cost)

## [1.10.0] - 2026-07-16

### Added

- added intent-aware review context: the pull request's description and commit count are fetched from the provider's REST API (GitHub: `GET /repos/{owner}/{repo}/pulls/{id}`; Azure DevOps: the PR resource plus its `/commits` collection) through a new provider-keyed `prmetadata` fetcher registry, and rendered — together with the title and branch names already in the prompt header — as a "Pull request context" section that instructs the AI to verify the diff against the stated intent, flag undocumented scope creep, and weigh the author's explanations before flagging intentional changes; the description is bounded to 16 KiB, escape-proofed against prompt injection, and the fetch is best-effort (never fails the review); controlled by the new tri-state `ai.pr_metadata` setting (`CODE_GURU_AI_PR_METADATA`, default on)

### Changed

- changed `CLAUDE.md` into a complete feature inventory and competitive landscape document, extracting every shipped capability (CLI, review pipeline, AI backends, rules and context system, trivial detection, webhook server, auto-merge gates, configuration, deployment, security hardening) and cataloguing the features peer products (GitHub Copilot code review, Claude Code Action / Claude Code Review, CodeRabbit, Qodo Merge, Greptile, Graphite, Sourcery, Amazon Q Developer, Gemini Code Assist) ship that Code Guru does not yet implement
- changed `docs/COMPARISON.md` to mark the "PR description context" backlog item as shipped and to record the remaining linked-issue slice
- changed the Go module dependencies to their latest versions

### Security

- resolved the Semgrep `incorrect-default-permission` CI failure on the auth config directory by documenting why owner-only `0o700` is the least-privilege mode for a directory (a directory needs the owner execute bit, so the rule's `0o600` file threshold would strip required access)

## [1.9.1] - 2026-07-14

### Changed

- changed the Go module dependencies to their latest versions

## [1.9.0] - 2026-07-13

### Added

- added automatic loading of the reviewed repository's own `CLAUDE.md` as project-specific review context on every provider (GitHub and Azure DevOps): when the file is not part of the PR diff, it is fetched from the repository's default branch, bounded to 32 KiB, and rendered into the AI prompt with prompt-injection framing so the review honours the project's documented conventions; controlled by the new tri-state `ai.project_guidelines` setting (`CODE_GURU_AI_PROJECT_GUIDELINES`, default on)

### Changed

- changed all AI backends (`openai`, `claude`, `anthropic`) to assemble the user prompt through a single shared `BuildUserPromptFor` helper, mirroring the existing `BuildSystemPromptFor` seam, so request-derived prompt sections cannot drift between backends
- changed the Go module dependencies to their latest versions
- changed the Go version to `1.26.5` and updated all module dependencies

## [1.8.5] - 2026-07-03

### Changed

- changed the Go module dependencies to their latest versions

## [1.8.4] - 2026-07-02

### Changed

- changed the Go module dependencies to their latest versions

### Fixed

- fixed an infinite re-review loop where a comment webhook for the bot's own `@code-guru` annotation re-triggered a review; the GitHub and Azure DevOps mention handlers now skip comments authored by the bot itself — matched against the configured `bot_identities` / `CODE_GURU_BOT_IDENTITIES` or the built-in `code-guru` / `code-guru[bot]` / `code-guru@<tenant>` shapes via `support.IsBotAuthor` — so an oversized diff that can never pass review no longer floods the pull request with repeated "review failed" comments

### Security

- replaced `secrets: inherit` with an explicit `CLAUDE_CODE_OAUTH_TOKEN` pass-through in the Claude Code workflows to satisfy the `secrets-inherit` least-privilege check

## [1.8.3] - 2026-06-19

### Changed

- changed the Go module dependencies to their latest versions

## [1.8.2] - 2026-06-18

### Changed

- changed the Go module dependencies to their latest versions

### Fixed

- fixed trivial-PR detection classifying a `CHANGELOG.md`-only change as `docs-only` (and as a dependency `update-*`), which let a version-bump PR be auto-approved even when the `bump-*` adapters were disabled — the changelog sits in every detector's allowed set, so the bump was claimed by whichever non-bump detector ran first. A change that touches only the changelog is now recognised as a version bump and is claimed **exclusively** by the `bump-*` detectors; `docs-only` and `update-*` still match when a genuine document or dependency manifest accompanies the changelog

## [1.8.1] - 2026-06-09

### Changed

- changed the `@code-guru` re-review to post its per-thread verdict as a reply **nested inside the existing thread** (via gitforge `ReplyToThread`) instead of a new comment on the same line, so the conversation reads continuously — like a human reviewer answering below the author's reply — rather than fragmenting into two parallel threads on one line. Falls back to a fresh inline comment when the provider gives no usable thread id. Pinned `gitforge` to the merged build carrying the new `ReplyToThread` method
- changed the Go module dependencies to their latest versions
- refreshed `CLAUDE.md` to document the `RetryingAIReviewer` decorator that `AIReviewerFactory` wraps around every AI backend and the `ai.max_attempts` config knob (env `CODE_GURU_AI_MAX_ATTEMPTS`)

## [1.8.0] - 2026-06-03

### Added

- added `bot_identities` configuration (env `CODE_GURU_BOT_IDENTITIES`, comma-separated) listing the account identities code-guru posts review comments under, so the `@code-guru` re-review conversation walk recognises its own prior threads even when a deployment posts under a service account whose name does not start with `code-guru`
- added `trivial.auto_merge_allowed_authors` (env `CODE_GURU_TRIVIAL_AUTO_MERGE_AUTHORS`, comma-separated) restricting trivial auto-merge to PRs opened by trusted automation accounts (e.g. `autobump` / `autoupdate` / config refresh); a human's docs PR is then approved but left for a human to merge instead of being force-merged past `Required reviewers`. An empty list keeps the prior any-author behaviour and logs a warning when combined with policy bypass. The Azure DevOps webhook handler now also populates `PullRequestDetail.Author` from `resource.createdBy` so the allowlist works on the ADO webhook path, not only on GitHub and the CLI
- added a re-review diagnostic that warns when a PR has existing comments but none are recognised as prior bot threads, pointing the operator at `CODE_GURU_BOT_IDENTITIES`
- added automatic retry of the AI review when the backend returns a non-JSON / unparseable response or a transient error (e.g. the Claude CLI's `"socket connection was closed unexpectedly"`): the backend is re-sampled up to `ai.max_attempts` times (env `CODE_GURU_AI_MAX_ATTEMPTS`, default `3`), reinforcing the "respond with ONLY valid JSON" instruction on each retry, instead of failing the review on the first blip. Applies to all backends (Claude CLI, Anthropic, OpenAI) via a `RetryingAIReviewer` decorator
- added self-detection of the bot's posting account from its own PR-wide review annotations (anchored to the start of the comment so a human who merely quotes the marker is not mistaken for the bot), so re-reviews recognise prior bot threads (and the author's replies) with no `bot_identities` configured

### Changed

- changed the "review failed" PR annotation to post a short, classified reason (e.g. "did not return a review in the expected JSON format") instead of the raw backend error, so a transient failure — notably the Claude CLI's JSON error envelope — is never surfaced as a comment on the pull request; the full error is logged for operators
- changed the Go module dependencies to their latest versions
- changed the Go version to `1.26.4` and updated all module dependencies
- changed the re-review prompt so a finding the author rebutted as intentional or not actionable (for example generated code or an established convention) is classified `resolved` and not raised again
- refreshed `CLAUDE.md` and `.github/copilot-instructions.md` to document the `bot_identities` configuration and the bot-identity recognition (`IsBotAuthor` / `DetectBotAuthors` self-detection) used by the `@code-guru` re-review conversation walk

### Fixed

- fixed `@code-guru` re-reviews repeating findings the author had already addressed or rebutted: the conversation walk recognised the bot's own comments only by the built-in `code-guru` name shape, so a deployment posting under a different account loaded an empty prior-conversation context and the AI re-reviewed from scratch on every pass

## [1.7.4] - 2026-05-25

### Changed

- changed the Go module dependencies to their latest versions

## [1.7.3] - 2026-05-22

### Changed

- changed the Go module dependencies to their latest versions

## [1.7.2] - 2026-05-20

### Changed

- changed the Go module dependencies to their latest versions

## [1.7.1] - 2026-05-19

### Changed

- changed the Go module dependencies to their latest versions
- refreshed `CLAUDE.md` and `.github/copilot-instructions.md` to document the webhook dispatcher, dedup cache/lease, source IP allowlist, ADO hydrator, health controller, and the four support utilities (`conversation.go`, `review_marker.go`, `verdict_mapper.go`, `truncate.go`) added since 1.5.0

## [1.7.0] - 2026-05-08

### Added

- added `docs/COMPARISON.md`, a side-by-side comparison of Code Guru against GitHub Copilot's PR review and Anthropic's Claude Code Action. The document includes a feature matrix across hosting, triggering, review output, re-review behaviour, model context, action capabilities, customisation, and operations; calls out where Code Guru leads (multi-vendor support, backend-agnostic, resolution-aware re-review, trivial PR fast path, native verdict submission, multi-pod ops primitives); and turns every gap into a prioritised improvement backlog (P0 — `suggestion` blocks, surrounding-file context, auto-skip uninteresting files, streaming progress, per-repo system-prompt overrides; P1 — fix-on-request commits, CI awareness, PR-description context, conversation continuation, severity routing, token-cost reporting, model fallback; P2 — cross-PR memory, GitLab/Bitbucket, multimodal, CWE/CVE enrichment, coverage delta, notification adapters, SARIF output, digest mode, chunked multi-pass review). Notable finding: GitHub's own published docs state Copilot "may repeat the same comments again, even if they have been dismissed" on re-review — exactly the failure mode the new resolution-aware path closes
- added `Settings.Trivial.AutoMerge` (env `CODE_GURU_TRIVIAL_AUTO_MERGE`, default `false`) and `Settings.Trivial.MergeStrategy` (env `CODE_GURU_TRIVIAL_MERGE_STRATEGY`, empty falls back to platform default) so a trivial-approve verdict can optionally complete the PR via `provider.MergePullRequest`. Off by default — the gate is "operator must explicitly opt in" because auto-merge bypasses human review and merges cross-system. A merge failure degrades gracefully: the trivial-approve verdict still stands and a warn log captures the error so the PR author can finish the merge manually. Pinned by `TestTrivialFastPathPostsSingleMarkerAndOptionalMerge` (auto-merge fires on approve, skipped on the default `false`, skipped on a `reject` verdict even with `AutoMerge=true`) and two new `TestNewSettings*` rows that verify env-overlay precedence
- added `Settings.Trivial.BypassPolicy` (env `CODE_GURU_TRIVIAL_BYPASS_POLICIES`, default `false`) so operators can opt the trivial fast path's auto-merge into ADO branch-policy bypass (`Required reviewers`, `Minimum approver count`, etc.). Surfaced live: `CODE_GURU_TRIVIAL_AUTO_MERGE=true` alone kept hitting `GitPullRequestUpdateRejectedByPolicyException` on internal repos with `Required reviewers` policies, because the bot's vote did not count toward the named-reviewer requirement. The new flag is intentionally separate from `AutoMerge` so deployments where the bot has merge permission but NOT the platform-level `Bypass policies when completing pull requests` permission still benefit from polite-merge auto-completion instead of every call becoming a hard 403. When both flags are true, `autoMergeTrivial` passes `gitforge.WithBypassPolicy("auto-merged by code-guru trivial PR policy")` so ADO's audit trail records the action with a non-empty reason. Pinned by three new rows in `TestTrivialFastPathPostsSingleMarkerAndOptionalMerge`: `AutoMerge` alone keeps `bypassPolicy=false`; both flags together set `bypassPolicy=true` with a non-empty reason; `BypassPolicy` alone is a no-op (the merge call never fires without `AutoMerge`)
- added resolution-aware re-review on the `@code-guru` mention path. The LLM now classifies every prior bot review thread shown in the conversation context as `resolved`, `outstanding`, or `outdated` (with a one-line explanation) BEFORE emitting any new inline `comments`. The post-pipeline forwards each verdict to gitforge: a single short reply per prior thread (carrying the explanation), plus an `UpdatePullRequestThreadStatus` call for `resolved` (mapped to `fixed`) and `outdated` (mapped to `closed`). `outstanding` keeps the thread `active`. New comments anchored to a `(file, line)` the LLM already classified are dropped automatically so the same finding never lands as both a thread reply and a parallel inline comment. This replaces the pre-existing behaviour where every re-review re-ran the diff review from scratch and flooded the PR with reworded duplicates of every prior finding. First-pass reviews and push-triggered reviews are unaffected — `ThreadResolutions` is empty when there is no conversation, so the prompt and output bytes match the previous shape exactly. Pinned by `TestApplyThreadResolutions` (resolved auto-closes, outstanding keeps active, outdated soft-closes, leading-slash normalisation, hallucinated anchors skipped, soft-fail on reply error still marks anchor handled), `TestExecuteMentionPathAppliesThreadResolutions` (end-to-end through `Execute`), `TestBuildResolutionReplyBody`, `TestMapResolutionStatusToThreadState`, `TestShouldCloseResolution`, plus a new conversation-walker row covering `ThreadID`/`RootCommentID` propagation and prompt-builder rows pinning the `thread_resolutions` schema field on both system templates

### Changed

- bumped `github.com/rios0rios0/gitforge` to `v1.0.1-0.20260504225531-2e5df4ac4fc2` (post-merge of [gitforge#96](https://github.com/rios0rios0/gitforge/pull/96)). The new revision supersedes the previous post-`#95` bump; both bumps land in this `[Unreleased]` section because no `code-guru` release was cut between them. The `#96` revision adds `entities.MergeOption` plus `entities.WithBypassPolicy(reason)` (consumed by the new `Trivial.BypassPolicy` flag above) on top of the `#95` `"rebaseMerge"` mapping. Without this bump, deployments setting `CODE_GURU_TRIVIAL_MERGE_STRATEGY=rebaseMerge` would still be silently downgraded to `squash`, and `CODE_GURU_TRIVIAL_BYPASS_POLICIES=true` would not compile against the older `MergePullRequest` signature. Pairs with the dev cluster Terraform change that adds `CODE_GURU_TRIVIAL_MERGE_STRATEGY=rebaseMerge` (and, in the matching toolbox PR, `CODE_GURU_TRIVIAL_BYPASS_POLICIES=true`) so the trivial fast path can complete PRs through Azure DevOps branch policies that require "Rebase with merge commit" as the only allowed strategy
- bumped Go to `1.26.3` and changed all Go module dependencies to their latest versions

### Fixed

- fixed 18 pre-existing `golangci-lint` issues surfaced by `golangci-lint v2.12.1` in CI: introduced package-level constants for the repeated string literals `approve`, `reject`, `comment`, `CHANGELOG.md`, `java`, `key`, and `lease_name` (16 `goconst` warnings); replaced the literal U+200B in `internal/support/prompt_builder.go` with the `​` escape sequence (one `staticcheck ST1018`); and removed the unused `//nolint:gosec` directive on `internal/infrastructure/controllers/webhooks/worker.go` (one `nolintlint`). No behaviour change — pure naming refactor and lint hygiene
- fixed Azure DevOps trivial detection where the leading `/` ADO prefixes onto every `GetPullRequestFiles` path (e.g. `/CHANGELOG.md`) bypassed the bump detectors' `.autobump.yaml` validation. The bump path compares incoming paths to its required-files set by exact string, so `/CHANGELOG.md` failed to match `CHANGELOG.md` and a valid bump PR on ADO would be auto-rejected as "missing CHANGELOG.md". `ReviewCommand.Execute` now passes every file through `normalizeFilePath` (single leading `/` stripped) before handing the slice to `handleTrivialDetection`. The regression test seeds an ADO-shape `/CHANGELOG.md` and asserts the registry receives the slash-stripped path
- fixed four issues in the resolution-aware re-review path surfaced by review of PR `#130`. (1) `buildConversation` now passes `nil` for the live-files filter so prior bot threads anchored to files the latest diff no longer touches still reach the LLM — those are precisely the threads the `outdated` resolution status exists for, and dropping them at the conversation stage was denying the LLM the chance to auto-close them. (2) `ThreadResolution` gained a synthetic `id` field (`T1`, `T2`, ...) populated in lockstep with the user prompt's `### Thread T<n> on <file>:<line>` headers; the post-pipeline now matches by id first and falls back to `(file, line)` only when that anchor identifies a single thread. Without the id, two prior bot threads on the same anchor used to collapse onto one map entry and silently lose every resolution past the first. (3) `applyThreadResolutions` now only adds an anchor to the `handled` set returned to `dropResolvedAnchorComments` when the resolution closes the prior thread (`resolved` / `outdated`); `outstanding` keeps the thread active, so a new comment on the same line is more likely a separate finding than a duplicate and suppressing it would be more aggressive than the duplicate-guard the dedup gate is meant to be. (4) `BuildSystemPrompt` no longer carries the `thread_resolutions` schema or the resolution rules on first-pass reviews — the new `BuildSystemPromptForReReview` (and the dispatcher `BuildSystemPromptFor`) scopes those additions to the path where a conversation actually exists, restoring the byte-for-byte first-pass invariant the original change claimed. Pinned by new rows in `TestApplyThreadResolutions` (id-based disambiguation of duplicate anchors; `handled` shape now reflects close-only inclusion), `TestBuildConversation` (stale-file thread reaches the prompt), `TestBuildSystemPrompt` (first-pass omits / re-review includes `thread_resolutions`; synthetic-id field present in the schema), and `TestBuildUserPromptWithConversation` (header carries `T1`)
- fixed the LLM-review path posting two PR-wide comments with the same summary text. `submitNativeReview` was called with `result.Summary` as its body, and `postReviewCompleteAnnotation` (since PR #124) also includes `result.Summary` in the annotation paragraph — so on Azure DevOps every LLM review left a duplicate summary on the PR (the native submission echo + the annotation paragraph). The native submission now passes an empty body, mirroring the trivial fast path; the annotation remains the canonical place for the rationale and is posted exactly once. Pinned by a new `TestExecuteLLMPathSubmitsNativeReviewWithEmptyBody` row using the existing `doubles.StubAIReviewerRepository` to drive the LLM path end-to-end and assert (a) the native submission's body is empty and (b) the annotation still carries `result.Summary`
- fixed the trivial fast path posting two PR-wide comments per review (the `[Auto-Approved]` body from `postApprovalComment` plus the body the native review submission echoed) AND not emitting the `**Code Guru review` substring the F2 review-once gate looks for. The combined effect on the dev cluster: a smoke `docs-only` PR got two reviews from two pods (four PR-wide comments total) — the first delivery's lease completed in ~1s, the second ADO `pullrequest.updated` event arrived 7s later after the lease released, found no marker, and re-ran the trivial path. `handleTrivialDetection` now posts a single completion-style annotation via `postReviewCompleteAnnotation` (carries the F2 marker) and submits the native review with an empty body so the reviewer-panel vote does not duplicate as a second comment. `postApprovalComment` and `postRejectionComment` are removed
- fixed the webhook dispatcher path losing trivial detection entirely. The DI provider in `internal/infrastructure/repositories/container.go` was hardcoded to `trivial.NewDetectorRegistry(nil)` regardless of `Settings.Trivial.Adapters`, and the dispatcher receives that registry by injection and never rebuilds it — so the configured adapters reached the `serve` controller's settings struct but never the dispatcher's registry. Surfaced live by a Markdown-only smoke PR on the dev cluster that still ran through the LLM despite `docs-only` being intended-on. The provider now depends on `*entities.Settings` and delegates to a new shared helper `trivial.NewDetectorRegistryFromConfig` which the CLI `review` controller (`review_controller.go`) also calls, so the two paths cannot drift again. Additionally, `NewSettings` (the YAML loader path used when `--config` or an auto-discovered `.code-guru.yaml` is present) now overlays `CODE_GURU_TRIVIAL_ADAPTERS` on top of the YAML-loaded adapter list — without this overlay, deployments that ship a baseline YAML and pin per-environment adapters via env would silently keep the YAML's list. Pinned by `TestNewSettingsTrivialEnvOverride` (env overrides YAML; unset env preserves YAML) and `TestNewDetectorRegistryFromConfig` (`Enabled=false` and empty `Adapters` both yield empty registries; non-empty registers the configured adapters)
- fixed trivial PR detection (`docs-only`, `bump-go`, `bump-node`, `bump-python`, `update-go`, `update-node`, `update-python`) so it actually runs in production. The gate at `internal/domain/commands/review_command.go` short-circuited the entire trivial path when `opts.CIPassed` was false, but every entry point — CLI (`review_controller.go`), GitHub webhook (`webhooks/github.go`), Azure DevOps webhook (`webhooks/azuredevops.go`) — hardcodes `CIPassed: false`. The gate was dead code that suppressed every detector since the feature shipped. Each detector self-validates what counts as "trivial enough" (bump detectors require a matching `.autobump.yaml`, `docs-only` requires every file be Markdown), so the CI gate was not load-bearing. Pinned by a new `TestExecuteRunsTrivialDetectionRegardlessOfCIPassed` test row in `commands/review_command_test.go` (a stub registry that always detects + an AI reviewer that panics if reached → `Execute` returns the trivial verdict with `CIPassed: false`)

### Removed

- removed the standalone PR-wide summary thread previously posted by `postComments` on no-inline-comments reviews (the `shouldPostSummary` gate), and removed the helper itself. The completion annotation built by `postReviewCompleteAnnotation` already carries `result.Summary` as a paragraph since PR #124, so leaving the standalone post in place duplicated the rationale on every clean review. Surfaced by Copilot review on PR #128 — without this removal the LLM-path empty-body fix only addressed half of the duplication. Pinned by extending `TestExecuteLLMPathSubmitsNativeReviewWithEmptyBody` to count occurrences of `result.Summary` across all PR-wide posts and assert exactly one (the annotation)

## [1.6.0] - 2026-05-03

### Added

- added a draft-PR skip so the bot no longer spends AI budget reviewing pull requests that the author has explicitly marked work-in-progress. `ReviewCommand.Execute` short-circuits with `verdict=comment` and a `"skipped: pull request is a draft"` summary BEFORE fetching files or posting the "reviewing" marker — a draft never accumulates a marker thread that would never get a completion annotation. Gated by the new `ai.review_drafts` setting (default `false`); flip via YAML or `CODE_GURU_AI_REVIEW_DRAFTS=true` to opt back in. The skip honours the `IsDraft` field on `forgeEntities.PullRequestDetail`; both webhook handlers (`webhooks/github.go` reads `pull_request.draft`, `webhooks/azuredevops.go` reads `resource.isDraft`) now propagate the flag onto the dispatched `Job`. Pinned by four new test rows in `webhooks/github_test.go` + `webhooks/azuredevops_test.go` (draft payload sets `Job.PR.IsDraft=true`, non-draft keeps it false) and two rows in `commands/review_command_test.go` (draft skip short-circuits with a draft summary; `ReviewDrafts: true` bypasses the gate)
- added a review-once-per-PR gate so the bot no longer floods the PR with a fresh review on every commit. `ReviewCommand.Execute` queries the PR's PR-wide comments via the new gitforge `ListPullRequestComments` and short-circuits with `verdict=comment summary="skipped: pull request has already been reviewed; mention @code-guru in a comment to request re-review"` when it finds an existing "Code Guru review complete" or "Code Guru review failed" annotation. The marker is the persistent state — no extra storage needed; the F2 substring (`**Code Guru review`) lives in the new `support.HasCompletedReviewMarker` helper. The "is reviewing" in-flight marker is intentionally NOT in the set: a webhook arriving while another pod is still reviewing should still go through the K8s-Lease cross-pod dedup rather than the marker gate
- added an inline-comment dedup pass so a re-review (the only path that can re-post inline comments after the F2 gate above) does not double up on findings the bot already posted. `ReviewCommand.dropDuplicateComments` queries existing inline comments via `ListPullRequestComments` and drops any new comment whose `(file, line, body[:200])` fingerprint already exists. PR-wide comments are NOT deduped (the F2 gate above already suppresses entire follow-up reviews; the only path that lands a duplicate PR-wide comment is the explicit `@code-guru` re-review the user asked for). Best-effort: a `ListPullRequestComments` failure logs at warn and falls back to posting every comment so the behaviour never regresses below today's no-dedup baseline. Pinned by 3 test rows: file+line+prefix match drops the duplicate, PR-wide comments pass through, leading-slash normalisation lets ADO `/internal/foo.go` match the AI's `internal/foo.go`
- added native pull request review submission so the bot's verdict surfaces in the platform's reviewer panel (Approved / Changes Requested / Waiting for Author) instead of only inside the completion annotation. After every trivial-detector verdict and every successful AI review, `ReviewCommand` now also calls `gitforge.SubmitPullRequestReview` — GitHub uses the `event` field on `POST /pulls/:n/reviews` (`APPROVE` / `REQUEST_CHANGES` / `COMMENT`), Azure DevOps uses the integer reviewer vote on `PUT /pullrequests/:id/reviewers/:reviewerId` (`10` / `-10` / `-5`). Gated by the new `ai.submit_native_review` setting (default `false`); flip via YAML or `CODE_GURU_AI_SUBMIT_NATIVE_REVIEW=true`. Best-effort: a provider failure logs at `Warn` and the existing text annotation still posts. The `comment` verdict maps to `WaitingForAuthor` (vote `-5` on Azure DevOps, soft `COMMENT` review on GitHub) so a clean AI run still surfaces a reviewer signal — see the later `### Fixed` entry for the second-pass behaviour change. The verdict-to-`forgeEntities.ReviewSubmission` translation lives in the new `support.MapVerdictToReview` helper, covered by table-driven unit tests in `internal/support/verdict_mapper_test.go`
- added prior-conversation context to the LLM prompt on `@code-guru` re-reviews so the model can read the bot's previous inline comments AND every reply on each thread before deciding whether to repeat / withdraw / respond to its own earlier findings. New `entities.ReviewRequest.Conversation` field carries the dialogue; new `support.BuildReviewConversation` walks `gitforge.PullRequestComment.InReplyToID` chains rooted on bot top-level comments (user-only threads and PR-wide markers are dropped); new `support.BuildUserPromptWithConversation` renders a "Prior review conversation" block with re-review guidance ("don't re-post when the user already addressed it; withdraw on correctly-identified false positives; respond inline only when the user asked a direct question") before the diff. First-pass reviews (`opts.UserMentioned == false`) leave `Conversation` nil so the prompt is byte-for-byte identical to its pre-conversation shape — proven by a test that asserts `BuildUserPrompt(...) == BuildUserPromptWithConversation(..., nil)`. All three AI backends (`openai`, `claude`, `anthropic`) now go through the conversation-aware variant. Best-effort: a `ListPullRequestComments` failure during the conversation walk logs at warn and falls back to nil so the re-review still runs (just without the dialogue context, which the F3 dedup will then handle by dropping any duplicates the LLM emits anyway). Pinned by 6 conversation-assembler test rows (empty/no-bot/walk-roots-only/reply-ordering/multi-hop/sort) + 3 prompt-rendering rows (no-drift on empty / block-before-diff / no-guidance-on-empty)
- added the `@code-guru` mention re-review path so users can request another review by posting a PR comment that mentions the bot. New `issue_comment` handler on the GitHub webhook (`HandleGitHub` dispatches by `X-GitHub-Event`) and new `ms.vss-code.git-pullrequest-comment-event` handler on the Azure DevOps webhook (`HandleAzureDevOps` dispatches by event type before the PR-lifecycle path). Both handlers parse the comment body via the case-insensitive word-boundary matcher in the new `support.HasMention` helper (rejects `@code-guru-staging` substrings; matches `@Code-Guru!` punctuation-terminated forms), then enqueue a `Job{UserMentioned: true}` so the review-once gate is bypassed downstream. The dispatcher's `HandlePR` gained a `userMentioned bool` parameter that wires through to `ReviewOptions.UserMentioned`. Comment-event payloads bypass the K8s-Lease cross-pod dedup entirely — a user posting `@code-guru` is an explicit re-review request and should always go through. Pinned by 4 GitHub test rows (mention-enqueues, no-mention-skips, edited-action-skips, non-PR-issue-skips) and 3 ADO test rows (mention-enqueues, no-mention-skips, off-allowlist-rejects)

### Changed

- bumped `gitforge` to the post-`ListPullRequestComments` revision (now includes the Azure DevOps `X-Ms-Continuationtoken` pagination fix and the GitHub provider's corrected `ThreadID` mapping that walks the `in_reply_to_id` chain instead of reusing `pull_request_review_id`) so the review-once gate, mention handler, and comment dedup all see the full comment list and group inline threads correctly
- bumped `gitforge` to the post-`SubmitPullRequestReview` connectionData-API-version fix (gitforge `#91`) so Azure DevOps' reviewer-ID lookup actually succeeds. Without this bump the `request_changes` and `comment` mapping additions from `#112` would still hit `failed to resolve reviewer ID: API error (status 400) VssInvalidPreviewVersionException` on every native review submission
- bumped `gitforge` to the post-`SubmitPullRequestReview` revision so the new `ReviewProvider.SubmitPullRequestReview` method and `PullRequestDetail.IsDraft` field are available; this is also the breaking-change boundary where Azure DevOps' `ListOpenPullRequests` stopped filtering drafts client-side (the policy now lives in `ReviewCommand`)
- changed the default of `ai.submit_native_review` from `false` to `true` so deployments that have not wired the flag pick up native pull request reviews (Approved / Changes Requested in the platform's reviewer panel) automatically. Operators that want the previous text-only behaviour now opt out explicitly with `submit_native_review: false` in YAML or `CODE_GURU_AI_SUBMIT_NATIVE_REVIEW=false`. Implemented as a tri-state `*bool` on `AIConfig.SubmitNativeReview` plus a new `AIConfig.NativeReviewSubmissionEnabled()` helper that resolves nil (the YAML / env "unset" state) to `true`; all three call sites (`review_controller.go`, `review_all_controller.go`, `webhooks/dispatcher.go`) now go through the helper. Pinned by six new test rows covering the resolver (`nil → true`, explicit `true`, explicit `false`) and the env-only path (unset → nil + default ON, explicit `false` → resolved off, unparseable typo → nil + default ON so a typo cannot silently flip behaviour)
- changed the Go module dependencies to their latest versions

### Fixed

- fixed orphaned dedup leases that blocked all webhook deliveries for a PR for up to 15 minutes after every pod restart. Captured live at `2026-05-02T00:18Z` where four leases held by terminated pods (`code-guru-6c8d774df7-gmjrb`, `-9schb`, etc.) blocked an internal PR and three other reviews for 12 minutes after a routine `kubectl rollout restart` cancelled long-running LLM reviews mid-flight (the deferred `ReleaseDedup` never ran because the `30s` drain budget killed the worker before it returned). Three changes together cap recovery at ≤60 seconds even for `kill -9` / OOM scenarios:
  - **Lease renewal loop**: `WebhookDedup` gained a `Renew(ctx, key)` method; the K8s-Lease backend implements it via the standard `Get` + `Update` round-trip (a full `Update` so the request carries the current `ResourceVersion` for conflict detection — the `LeaseClient` interface intentionally stayed small and does not need a separate `Patch` verb). The `serve` controller's worker handler kicks off `go d.RenewDedup(ctx, job.DedupKey)` for every in-flight job; the loop ticks every `30s` and exits when the job context is cancelled. Healthy pods keep their leases held; dead pods stop renewing and the lease becomes takeover-eligible within `leaseDurationSeconds`. The in-memory backend implements `Renew` as a no-op (per-pod TTL already covers the worst case).
  - **`leaseDurationSeconds` reduced from `900` → `60`**: with the renewal loop above, the freshness window only needs to outlive a single renew tick + one API timeout (compile-time invariant pinned via a static `const _ = ...` assertion in `dedup_lease.go`), not the whole review wall-time. Recovery from a crashed pod drops from `15min` → `≤60s` even when no graceful shutdown happens.
  - **Mass-release on shutdown**: the `Dispatcher` now tracks every dedup key it has acquired in an `inFlight map[string]struct{}` (mutex-guarded) and exposes `ReleaseAllInFlight(ctx)`; the `serve` controller calls it after `pool.Shutdown` returns. Graceful drains release every lease immediately so the next webhook on a fresh pod re-acquires without any wait.
  - Pinned by 4 new test rows (`TestK8sLeaseDedupRenew` covers Update success / API-error keep-loop-alive / no-op-on-released, `TestLeaseDurationAndRenewIntervalInvariant` static-asserts the `duration > renew + apiTimeout` relationship), 3 new dispatcher rows (`TestRenewDedupLoopExitsOnContextCancel`, `TestReleaseAllInFlight` no-op + idempotency + error-swallow, `TestRenewIntervalInvariantPreCheck`), plus updated existing rows where the `2000s` aged-lease setup was relative to the old `900s` window
- bumped the default `Server.ShutdownTimeout` from `30s` → `90s` so most reviews flush their inline-comment posts and completion annotation cleanly on `SIGTERM`. The bound is still well within K8s' default `terminationGracePeriodSeconds` (typically 300s, tunable per Pod), and the renewal-loop + lease-duration safety net (`≤60s` recovery) bounds whatever the drain budget cannot finish
- changed the `comment` verdict to map to `forgeEntities.ReviewVerdictWaitingForAuthor` so Azure DevOps surfaces it as vote `-5` ("waiting on the author") and GitHub posts a soft `event=COMMENT` review (gitforge `WaitingForAuthor` → GitHub `COMMENT`). Previously the `comment` verdict skipped the native review entirely, which meant clean AI runs left the bot's reviewer state untouched even when the platform supported a native "I have something to flag, no formal vote" signal. Pinned by table-driven test rows in `internal/support/verdict_mapper_test.go` and a `commands/review_command_test.go` row that asserts the recorded submission carries `WaitingForAuthor`
- fixed `support.MapVerdictToReview` so the LLM-vocabulary `request_changes` verdict (emitted by `internal/support/response_parser.go`) translates to `forgeEntities.ReviewVerdictRequestChanges` instead of silently skipping the native review submission. Captured live in dev pod logs at `2026-05-01T21:13Z` where every PR with `verdict=request_changes` (including `<internal-repo>#NNNN` and `<internal-repo>#NNNN`) finished with the text annotation but never set the bot's vote in the reviewer panel — the mapper only knew the trivial-detector vocabulary (`reject`) and the bug was operationally invisible because it had no error to log

## [1.5.0] - 2026-05-01

### Added

- added a "Code Guru is reviewing this PR" PR-wide marker comment that the bot posts as soon as it picks up a non-trivial PR. Closes the gap between webhook-receive and review-complete (which can be 5-10 minutes on complex diffs), removing the failure mode observed on `internal-terraform/internal-customer-app#NNNN` on `2026-05-01` where the author merged at the 7-minute mark and missed the 3 well-grounded review comments that landed 4 minutes later. The marker is placed AFTER the trivial-detection gate so auto-approve / auto-reject PRs (already-instant signal) don't get the extra noise; skipped under `DryRun`. Best-effort: a failure to post the marker logs at `Warn` and the review continues — the marker is operator-visible UX, not a correctness gate. The body is rendered by a pure helper `buildReviewingMarkerBody(time.Time)` (exposed via `export_test.go`) so the formatting contract is unit-testable; three new test rows pin (1) RFC 3339 UTC timestamp embedded so the operator log and the PR thread carry the same shape, (2) zero-time defensive fallback (non-empty body), (3) real `\n\n` newlines (not the literal escape sequence — required for Markdown paragraph rendering on both ADO and GitHub)
- added a "Code Guru review complete" PR-wide annotation that the bot posts AFTER the inline + summary comments land, so the PR author sees an explicit "done" signal closing out the "reviewing" marker (PR `#102`). Without this, the marker stays open-ended and the author has to count comments to infer that the bot finished — observed live on `internal-terraform/internal-customer-app#NNNN` on `2026-05-01` where the author merged before realising the bot had finished and missed the 3 review comments. The body surfaces (1) the verdict (`approve` / `comment` / `request_changes`) so the conclusion is visible at a glance, (2) the count of inline comments with correct pluralisation (`1 inline comment` vs `0 inline comments`), and (3) the completion timestamp in the same RFC 3339 UTC shape the marker uses so a reader can pair them. Best-effort: wrapped in the same `5s` `context.WithTimeout` (`reviewingMarkerPostTimeout`) as the marker, with a `Warn` log on failure that never blocks the worker. Six new unit-test rows in `TestBuildReviewCompleteBody` pin the formatting contract: rendered headline + verdict + comment count + timestamp, singular `1 inline comment`, plural-for-zero `0 inline comments`, empty-verdict fallback to `comment`, non-UTC normalisation to UTC (mirroring the `buildReviewingMarkerBody` defensive contract), nil-result graceful degradation. Note: this is the **simple** completion-notice variant; the polished version (mark the original marker thread as `fixed` instead of posting a separate notice) is blocked on a gitforge `UpdateThreadStatus` method and tracked separately
- added a "Code Guru review failed" PR-wide annotation that the bot posts when the AI step crashes, so the PR author understands the silence after the "reviewing" marker is a failure rather than the bot still working. Without this annotation (and combined with the dedup gate from PR `#100` — only one pod attempts the review per PR), a single `claude CLI failed: exit status 1` left the PR with the marker promising a review that never arrives — the exact failure mode observed live across `internal-terraform/internal-customer-app#NNNN`, `internal-app/internal-integrator#NNNN`, `internal-terraform/internal-customer-app#NNNN` on `2026-05-01` where ~half of all reviews failed silently. The body surfaces both the timestamp (so the operator can correlate with the pod log line) and the underlying error text (truncated and quoted via `support.TruncateForLog` to a `2 KB` cap so a runaway claude cannot flood the PR thread). Best-effort: the post is wrapped in the same `5s` `context.WithTimeout` as the "reviewing" marker (`reviewingMarkerPostTimeout`), and a failure to post the annotation logs at `Warn` while the original `reviewErr` still bubbles up to the worker. Four new unit-test rows in `TestBuildReviewFailedBody` pin (1) the headline + timestamp + error text appear in the rendered body, (2) non-UTC input is normalised to UTC inside the helper (mirroring the `buildReviewingMarkerBody` defensive contract), (3) a nil error renders as `(no error details)` instead of the literal `<nil>`, (4) a 10 KB oversized error is truncated to fit within a 4 KB rendered body with the `...[truncated]` sentinel attached
- added a cross-pod webhook dedup backend backed by Kubernetes `Lease` objects (`coordination.k8s.io/v1`) so the bot's `replicas: 2` AKS deployment no longer produces duplicate reviews when Azure DevOps fires both `git.pullrequest.created` and `git.pullrequest.updated` and the K8s `Service` round-robins one delivery to each pod. The previous in-memory `webhookDedupCache` from PR `#100` was per-pod and could not see across replicas — both pods would independently process, run the AI, and post comments (live across `internal-terraform/internal-customer-app#NNNN..#NNNN` on `2026-05-01`). Operator-rejected alternatives were `replicas: 1` (gives up availability) and dropping one ADO subscription (loses coverage). The new `WebhookDedup` interface in `dedup_cache.go` abstracts both backends behind `SeenRecently(ctx, key) bool` + `Forget(ctx, key)`; the in-memory implementation stays the default for tests and local CLI runs, the new `K8sLeaseDedup` in `dedup_lease.go` is wired automatically by the `serve` controller whenever `KUBERNETES_SERVICE_HOST` is set. The lease dance uses the K8s API server's optimistic concurrency: each pod tries `Create` on a `Lease` named `code-guru-{sanitised-key}-{sha256-prefix}` (the SHA-256 suffix prevents distinct keys from colliding under the lossy `[^a-z0-9-] -> -` substitution) with `spec.leaseDurationSeconds=900` (must exceed the bot's maximum review wall-time — observed outlier ≈8 minutes — so the takeover path never steals an actively-held lease, which would itself produce the duplicate the dedup is meant to prevent); the first `Create` returns `201` and the pod owns the review, every concurrent `Create` returns `409 AlreadyExists` and the pod returns `200 OK` without enqueueing. Kubernetes does NOT auto-delete `Lease` objects when `leaseDurationSeconds` elapses, so the dedup contract relies on two explicit pieces of work: (1) the worker pool handler in the serve controller `defer`s a release of the dedup record after every job (success or failure) — without this a successful review would leak its lease in etcd forever and block all future deliveries for the same PR; (2) when `Create` returns `AlreadyExists`, `SeenRecently` runs a stale-lease takeover — `Get` the holder, check whether `acquireTime + leaseDurationSeconds` (or `renewTime + duration`, whichever is later) has already passed, and if so `Delete` the stale lease (with a UID precondition so a concurrent renewer cannot lose work) and retry `Create`. The duration is therefore the upper bound on how long a crashed pod's lease can block new work. Every API call is wrapped in a `5s` `context.WithTimeout` so a wedged control plane never stalls webhook delivery; on any non-`AlreadyExists` error the dedup degrades to "process the webhook" — never WORSE than the per-pod cache baseline. Eight unit-test rows in `dedup_lease_test.go` (using a hand-rolled fake `LeaseClient` so the test file does not pull in `client-go/kubernetes/typed/coordination/v1/fake`) pin (1) `Create` succeeds → `false` returned + lease persisted, (2) `Create` returns `AlreadyExists` from a second pod with a fresh lease → `true` returned + the takeover path runs `Get` once and does NOT delete, (3) stale lease (acquired far past `leaseDurationSeconds`) → takeover deletes + re-acquires, (4) `Forget` after acquire → next `SeenRecently` re-acquires + `Delete` was called, (5) non-`AlreadyExists` `Create` error → falls through with a `Warn` log, (6) `Forget` on a `NotFound` is idempotent (no panic, dedup stays usable), (7) two GitHub keys that differ only by a `/` vs `-` boundary produce distinct lease names (collision-resistance), (8) sanitisation transforms `ado:abc-uuid:12345` and `gh:Org-Name/Repo-Name:99` into RFC 1123 lease names (lowercase, alphanumeric + `-`, ≤ 253 chars, ends with alphanumeric, prefixed with `code-guru-`). **Operator action required:** the bot's ServiceAccount must be granted a namespace-scoped `Role` + `RoleBinding` on `coordination.k8s.io/leases` with verbs `get`, `list`, `create`, `delete`, `update`, `patch` (the exact YAML lives in the `dedup_lease.go` package doc and in the README "Kubernetes deployment" section). Without the RBAC the in-cluster wiring fails, the controller logs `Warn`, and the dispatcher falls back to the per-pod in-memory cache — operationally safe (still gates ADO retry storms that loop back to the same replica) but loses the cross-pod gating
- added a mid-flight PR-status re-check in `ReviewCommand.postComments` so the bot no longer posts comments on a PR that was completed / abandoned / merged / closed between the webhook delivery and the AI review finishing. Captured live on `internal-terraform/internal-customer-app#NNNN` on `2026-05-01` where the author merged at the 7-minute mark and the bot's 3 review comments still landed on the merged PR — operator-visible noise that wasted PAT quota. The new helper `isPullRequestClosed` calls `provider.GetPullRequestStatus` (newly exposed on `gitforge` per PR `#86`) and returns true when the status is one of `completed`, `abandoned`, `closed`, `merged` (case- and whitespace-tolerant). Best-effort: a fetch failure logs `Warn` and proceeds with the post — the bot is never worse than today's baseline. Twelve new test rows in `TestIsPullRequestClosed` pin (1) all four closed-status values across both providers, (2) case + whitespace normalisation, (3) `active` / `open` / empty / unknown-future-enum values are NOT closed, (4) a fetch error defaults to `not closed` so the caller proceeds with posting. The helper is wired through a narrow `pullRequestStatusGetter` interface (exported via `export_test.go`) so the tests can use a 1-method stub instead of a full `forgeEntities.ReviewProvider`
- added a structured allowlist-rejection diagnostic on `HandleAzureDevOps` that emits the parsed `event_type` / `pull_id` / `status` / `repo_id` / `repo_name` / `remote_url` / `project_name` / `parsed_org` plus a 32 KB head of the raw request body — useful when the ADO management notification API shows a fully populated `resource.repository` but the live HTTP body delivered to the pod parses with empty fields (live diagnosis on `internal-terraform/internal-customer-app#NNNN`). The body cap is `32768` bytes (was `4096`) so the cut lands AFTER the `resource` block instead of right before it — at 4 KB the verbose `message` / `detailedMessage` HTML+markdown blocks consumed the entire budget, leaving `resource` truncated and the diagnostic useless. Surfaced at `Warn` (was `Debug`) because the rejection itself is an operator-level signal and `kubectl set env DEBUG=true` keeps getting reverted by `terra apply` runs that race with the diagnosis loop — `Warn` survives whatever the pod's log level is set to. **Renamed log field `body_head_4kb` → `body_head`** in lockstep with the cap change so the name does not lie about the size; consumers who built log queries against the previous `body_head_4kb` key need to re-key. The previous name was only in the codebase for hours (introduced in the same `[Unreleased]` window) and was never emitted to production logs at `Info` level, so a forwards-only rename is acceptable rather than carrying a dual-key alias
- added an `ADOResourceHydrator` interface plus the production `httpADOHydrator` that fetches the full PR envelope from the Azure DevOps REST API whenever a webhook delivers the **stripped-down** `resource` block (only `pullRequestId` + `url`). Empirically captured against subscriptions `subscription-A` and `subscription-B` where 40/40 deliveries arrived without `resource.repository`, `resource.status`, `resource.sourceRefName`, or `resource.targetRefName` — ADO **org-wide** subscriptions emit this skinny shape regardless of `resourceVersion` or `messagesToSend`. Without hydration, the allowlist check sees `org=""` and `project.name=""`, the bot 403's every event, and the subscriptions go onProbation after the consecutive-4xx streak. The hydrator runs immediately after the closed-status guard in `HandleAzureDevOps`, uses the configured Azure DevOps PAT (Basic auth with empty username), applies a `10s` per-call timeout, and uses `api-version=7.1-preview.1` (lowest version that returns `repository.project.name`). The subsequent allowlist + worker code path is unchanged because every consumer keeps reading the canonical `event.Resource` fields. The hydrator is wired through `Dispatcher.SetADOHydrator` so tests substitute a stub `httptest.Server` instead of touching the network. Three new unit-test groups (`TestIsSkinnyADOResource`, `TestAppendAPIVersion`, `TestMergeHydratedADOResource`, `TestHTTPADOHydrator`) pin the detection predicate, the URL builder, the merge precedence (hydrated wins, original is fallback), and the end-to-end HTTP contract (success, non-2xx error, empty-token rejection, malformed-URL rejection)
- added an external `webhooks_test` test file (`azuredevops_internal_test.go`) plus an `export_test.go` re-exporter that pin the contracts of the unexported helpers `extractADOOrganization`, `isClosedADOPullRequestStatus`, `isSupportedADOEvent`, `refToBranch`, and `truncateForLog`. Coverage includes every URL shape ADO has been seen to deliver (`https://dev.azure.com/Org/...`, the userinfo form `https://Org@dev.azure.com/...` captured live on PR #NNNN, legacy `*.visualstudio.com`, regional sub-domains, malformed/empty), case + whitespace normalisation on `status`, the case-sensitivity contract on event types, the tag-ref defensive non-strip in `refToBranch`, and the byte-based truncation budget in `truncateForLog`. Each row carries a comment explaining the production bug it pins so a future "let me clean this up" refactor surfaces in the test before it ships
- added six handler-boundary integration tests in `azuredevops_test.go` that pin the dual-shape contract (project-scoped full payload AND org-wide skinny payload + hydration). Coverage: (1) skinny payload → hydrator called with the resource URL + configured PAT → worker receives the canonical `Repo.ID`/`Project`/branches; (2) full payload → hydrator NOT called (counter assertion that prevents a future "always hydrate" regression from doubling the API hop on every project-scoped delivery); (3) hydrator returns an upstream error → 502 (`Bad Gateway`) so ADO's circuit breaker treats it as a transient upstream issue rather than counting it toward the consecutive-4xx probation budget; (4) hydrated payload reports an `abandoned` PR → 204 (re-applies the closed-status guard against the hydrated value, since the skinny shape carries `status=""` and lets the early guard pass); (5) skinny payload + no PAT configured → 500 with the hydrator NOT called; (6) hydrated payload reveals a project off the allowlist → 403. The stub hydrator (`stubADOHydrator`) records every call and the URL/token it received, so each row asserts both the response code AND the hydrator invocation count
- added two `Debug`-level log lines in `hydrateSkinnyADOResource` that record which subscription mode each delivery used (`PR #X arrived with full resource block (project-scoped subscription) — skipping hydration` vs `PR #X arrived with skinny resource block (org-wide subscription) — hydrating via REST API`). Operationally this lets us confirm from logs alone which subscription type is firing without grepping the raw payload — useful for fleet-wide migrations between project-scoped and org-wide subscriptions

### Changed

- changed `gitforge` to `v0.9.7-0.20260501035717-b1628aba383f` (post-merge pseudo-version covering both `gitforge#85` — `iterationContext` + `changeTrackingId` on ADO thread payload — and `gitforge#86` — new `UpdatePullRequestThreadStatus` and `GetPullRequestStatus` methods, plus a **breaking** change to `PostPullRequestThreadComment` returning `(int, error)` instead of `error`). Code-guru's call site at `internal/domain/commands/review_command.go` now discards the new thread ID with `_` because the inline comment threads are not yet updated post-creation; the marker thread (PR `#102`) is the auto-close candidate but goes through `PostPullRequestComment` (signature unchanged). Once the polished marker auto-close ships, that path will start consuming the captured thread ID
- changed `ReviewCommand.postComments` to suppress the PR-wide summary thread whenever the review carries one or more per-issue comments (inline `Line > 0` threads or PR-wide `Line <= 0` annotations); observed on `internal/warden-service#NNNN` where every push produced a duplicate summary thread on top of the per-file inline threads, so reviewers saw a fresh "summary" comment accumulate next to the same per-issue feedback after each commit. The summary is still posted when `result.Comments` is empty (clean reviews / `verdict=approve`) so the operator retains a visible signal that the bot ran. Decision is centralised in `commands.shouldPostSummary` (exposed via `export_test.go`) so the gate is unit-testable without standing up the full `Execute` flow with stubs
- changed `support.ParseReviewResponse` so it no longer falls back to `&ReviewResult{Summary: content}` on a parse failure — that fallback is what allowed the malformed-JSON dump described below to reach the PR. Callers that depended on the previous "always returns a result" contract (the three AI reviewer repositories — `claude`, `openai`, `anthropic`) propagate the new error up through `ReviewDiff`; the worker layer already logs and swallows reviewer errors, so a parse failure now manifests as a log line and an absent comment rather than a noisy thread
- changed every PR-wide informational annotation (the "Code Guru is reviewing" marker, the "Code Guru review complete" notice, and the "Code Guru review failed" notice) to be posted with `forgeEntities.WithThreadStatus("closed")` so Azure DevOps renders them as ended discussions instead of active threads the PR author has to dismiss by hand. Resolves the operational nit observed live on every successful review since PRs `#102`/`#103`/`#104` shipped: every PR ended up with two unresolved "informational" threads (the marker + the completion notice) that the author had to click through individually before the PR could merge with a clean thread state. GitHub's REST review surface has no per-comment thread status and silently ignores the option (documented in the `forgeEntities.WithThreadStatus` doc comment), so this is effectively ADO-only behaviour with provider-agnostic wiring — the call sites do not need a provider switch. The new package constant `annotationThreadStatus = "closed"` (re-exported as `commands.AnnotationThreadStatus` via `export_test.go`) is the single source of truth for the value, so a future "let me try `fixed` instead" refactor only touches one line. Required `gitforge` `>=0.9.7-pseudo-2e59f6e` (PR `rios0rios0/gitforge#87`) which introduces the `entities.CommentOption` functional-options pattern. Two new test groups in `review_command_test.go`: `TestAnnotationThreadStatusContract` pins the constant value with the exact reasoning in the assertion message, and `TestMarkerHelpersForwardThreadStatusOption` drives all three helpers through a recording `forgeEntities.ReviewProvider` stub (embedded zero-value interface so any unused method panics — guarding against silent stubbing of a future helper that takes a different code path) and asserts the resolved status string lands as `closed` on every call
- changed the Go module dependencies to their latest versions
- changed three subtests across `azuredevops_hydrator_test.go` and `dedup_cache_test.go` to carry the full BDD `given / when / then` triplet that CLAUDE.md requires on every subtest. Two rows in `TestAppendAPIVersion` (`should reject a relative URL`, `should reject a URL with a control character that fails url.Parse`) and one in `TestWebhookDedupCache_SeenRecently` (`should be safe under concurrent calls on the same key`) had been merged in earlier PRs (`#97`, `#100`) without the marker block; the gap was caught by a repo-wide audit script. After this change the audit reports zero missing markers across all seven recently-touched test files. Test-only change with zero behaviour impact; included to keep the BDD-compliance baseline at 100%

### Fixed

- fixed `code-guru` posting the raw model output as a single PR-wide thread when the AI returned malformed JSON; observed on `internal/auth-service#NNNN` thread `71418` where the model emitted `"body":"... Rule: Go Logging — "Always use \`WithFields\` ..."."` with unescaped `"` characters inside the string value. `json.Unmarshal` rejected it, the markdown-fence regex missed (the model honoured the "no fences" instruction), and `ParseReviewResponse` defaulted to `Verdict="comment"` plus `Summary=raw response` — which `postComments` then dumped onto the PR as a 3.5 KB JSON blob. Added a `repairJSONStrings` state-machine pass that escapes any `"` whose lookahead is not a JSON structural token (`,`, `:`, `}`, `]`, or end of input); valid JSON round-trips unchanged. On total parse failure the parser now logs the raw content (truncated to `4096` bytes) at `ERROR` and returns `support.ErrUnparseableResponse` so the worker logs the failure and posts nothing — instead of fabricating a comment from the broken response
- fixed `HandleAzureDevOps` 204'ing every webhook delivery whose `resource.status` was empty, dropping every push silently — diagnosed live on `internal-terraform` PR #NNNN where ADO's `git.pullrequest.updated` payload shipped `status: ""` on commit-only updates. The handler used to require an exact `status == "active"` match, which rejected the empty value (and any future enum value Azure DevOps may add). Replaced the strict-active check with a reject-only-known-closed predicate `isClosedADOPullRequestStatus` that returns `true` only for `abandoned` / `completed` (case- and whitespace-tolerant); empty/missing/unknown values now proceed. Two new test cases lock the contract: `should respond 204 (No Content) when the PR is completed` and `should respond 202 (Accepted) when status is empty`, alongside the existing abandoned-status test
- fixed `ReviewCommand.postComments` posting inline review threads against files that had been removed from the PR between the diff snapshot and comment delivery, producing the ADO "this file no longer exists in the latest pull request changes" warning on every comment. Captured live on `internal-app/internal-integrator#NNNN` where every bot comment carried the banner because the PR had been rewritten (squash / rebase / follow-up push) between the webhook firing and the review completing. The fix re-fetches `provider.GetPullRequestFiles` immediately before posting, builds a normalised path set (matching the `support.LookupChunkByPath` rule of stripping a single leading `/`), and partitions the AI's findings into kept (file still in the latest iteration) vs dropped (file no longer present). Dropped comments are summarised at `Warn` with up to eight unique paths and a `(+N more)` sentinel so the operator log line stays bounded under a squash that rewrites the entire PR. The check is **best-effort**: if `GetPullRequestFiles` itself fails (transient ADO outage, expired PAT) the bot falls back to posting all comments — never worse than today's baseline. Three exported helpers (`filterStaleComments`, `summarizeStaleFilePaths`, `normalizeFilePath`) are pure functions wired through `export_test.go`; 14 new unit tests cover (1) all-paths-still-live → none dropped, (2) some-stale → only the right ones dropped, (3) empty-live-set → everything dropped, (4) ADO-style `/internal/foo.go` matches AI-style `internal/foo.go`, (5) AI-supplied leading-slash also normalises, (6) empty input → empty output, plus 3 rows on the summariser (dedupe, cap-with-sentinel, empty input) and 5 rows on `normalizeFilePath` (single-strip, no-op, trailing slash, empty, double-leading)
- fixed every Claude CLI failure being logged as an opaque `claude CLI failed: exit status 1 (stderr: )` because `AIReviewerRepository.ReviewDiff` discarded the child process's stdout on a non-zero exit. `claude --print --output-format json` writes its **error envelope** to stdout by contract (the JSON the CLI promises to return); throwing that stream away hid the only diagnostic the CLI actually produced. Captured live across PRs `#NNNN` / `#NNNN` / `#NNNN` / `#NNNN` / `#NNNN` on `2026-05-01` where roughly half of all reviews failed silently with no operator-visible cause. The wrapper now wraps both streams into the error message via `support.TruncateBytesForLog` (`claudeFailureLogLimit = 4096` bytes per stream so the resulting log line stays bounded; quoted via `strconv.Quote` so newlines / tabs cannot inject log lines). The byte-slice variant is used so an oversized failure (megabytes of runaway log) is not stringified before truncation. Three new regression tests in `claude_ai_reviewer_repository_test.go` drive the real `ReviewDiff` against a `/bin/sh` fake-binary fixture (a `t.TempDir()` script controlled via `FAKE_STDOUT` / `FAKE_STDERR` / `FAKE_EXIT` env vars): (1) JSON envelope on stdout + auxiliary message on stderr → both surface in `err.Error()`, (2) stderr-only failure → no regression of the existing capture, (3) 8 KB stdout → captured and truncated below the cap with the `...[truncated]` sentinel asserted. Tests skip on Windows because the fake binary uses `/bin/sh`
- fixed every newly-created PR getting two duplicate review threads because Azure DevOps fires both `git.pullrequest.created` AND `git.pullrequest.updated` for the same PR creation, and the K8s `Service` load-balanced each delivery to a different pod replica — both pods enqueued, both ran the AI, both posted. Captured live across `internal-terraform/internal-customer-app#NNNN`, `internal-terraform/pipelines#NNNN`, and `internal-terraform/internal-customer-app#NNNN` on `2026-05-01` where every PR ended up with two near-identical bot threads on the same file:line. The fix adds a per-pod TTL cache on the dispatcher, keyed by `provider:repo_id:pr_id` (or `provider:owner/repo:pr_id` for GitHub) with a `30s` TTL — the longest sibling-delivery gap we observed was 4 seconds, so 30 s gives headroom for ADO retry storms while staying far below any realistic real-follow-up-push interval (which is minutes, not seconds). Both webhook handlers (`HandleAzureDevOps` and `HandleGitHub`) consult the cache after the closed-status guard but before `submitter.Submit`; duplicates return `200 OK` (acknowledged but not enqueued) with a `Debug` log line. **Cross-pod limitation:** the cache is per-pod, so when ADO's two distinct events land on two different replicas the cache cannot dedup them — that needs either a single replica in the toolbox terraform or a single ADO subscription. Both are tracked as deployment-side follow-ups; the per-pod cache still helps with ADO's webhook retry storms (which can route the retry back to the same pod) and is the foundation for any future shared-state dedup. Six new unit tests in `dedup_cache_test.go` (run under `-race`) pin (1) first-call passes, (2) within-TTL duplicate is caught, (3) post-TTL refresh, (4) distinct keys are independent, (5) `TTL=0` disables the cache, (6) 50-goroutine concurrent calls on the same key — exactly one returns "not seen". Two new handler-boundary integration rows in `azuredevops_test.go` pin the wiring contract: (a) duplicate-payload sequence → first delivery enqueues, second returns 200 without enqueueing, only one job survives; (b) two distinct PR IDs in quick succession → both enqueue (counter assertion preventing a future "let me widen the dedup key" regression).

## [1.4.0] - 2026-04-29

### Added

- added `clientIP(*http.Request)` and `sourceIPAllowed(ip, prefixes)` helpers in `internal/infrastructure/controllers/webhooks/source_ip.go`, plus a `Dispatcher.enforceSourceIPAllowlist` middleware-style helper that both webhook handlers call as their first guard
- added `deliver_docker: true` to `.github/workflows/default.yaml` so future tag pushes automatically build and publish the Docker image to `ghcr.io/rios0rios0/code-guru` alongside the binary release; previously every image bump required a manual `docker build && docker push` (see the `0.2.0` rollout for the toolbox stack)
- added `packages: 'write'` to the workflow `permissions:` block so the `delivery-docker` job can authenticate to GHCR; reusable workflows cannot escalate beyond the caller's grants, so the permission has to be declared at the caller level
- added `Server.AllowedSourceCIDRs` (env: `CODE_GURU_SERVER_ALLOWED_SOURCE_CIDRS`) — a comma-separated CIDR allowlist enforced on `/webhooks/azuredevops` and `/webhooks/github` before any auth check; the source IP is read from `CF-Connecting-IP`, then `X-Real-IP`, then the leftmost `X-Forwarded-For` entry, then `RemoteAddr` (in that order); empty list means "no allowlist", preserving existing behaviour. CIDRs are parsed once at dispatcher construction so the per-request hot path has no parsing cost; invalid entries are logged and skipped

### Changed

- changed `BuildSystemPrompt` to fall back to a general best-practices system prompt when no rules are loaded; the previous template embedded an empty `Rules to enforce` block plus the instruction `Do NOT comment on style preferences not covered by the rules`, which made the LLM correctly produce zero comments on every PR when `CODE_GURU_RULES_PATH` was unset or no rules matched the file languages — the no-rules path now asks the model to review for bugs, security issues, performance problems, and clear correctness violations without referencing a non-existent rule set
- changed both system prompt templates to include `"verdict": "approve"` in the no-issues example so the LLM does not omit the field on clean reviews; without this, `ParseReviewResponse` defaulted to `comment` and downstream automation could never reach a clean `approve` verdict
- changed the `Dockerfile` `SHELL` directive from `["/bin/bash", "-c"]` to `["/bin/bash", "-eo", "pipefail", "-c"]` so `pipefail` is enforced at the shell level for every `RUN` (the inline `set -euxo pipefail` becomes redundant defense in depth) — fixes hadolint `DL4006` triggered by the `claude --version | tee /etc/claude-version` pipe added in `1.4.0`
- changed the Go module dependencies to their latest versions

### Fixed

- fixed `default / delivery > docker` job failing on `main` with `ERROR: failed to build: resolve : lstat .ci: no such file or directory` after `rios0rios0/pipelines` commit `c9553e2` (`hotfix(moved): moved Dockerfile of original position`) renamed the convention to `.ci/stages/40-delivery/app.Dockerfile`; relocated the existing `Dockerfile` to that path. Build context stays at the repo root so all `COPY` directives resolve unchanged
- fixed `Repository ID is empty, falling back to repository name for API calls` warning emitted by the gitforge Azure DevOps provider on every webhook delivery; the ADO `git.pullrequest.created` / `updated` payload includes the repository UUID at `resource.repository.id` but the handler was not extracting it. Added `ID` to the `adoRepository` struct and now passes `forgeEntities.Repository{ID: ...}`. The fallback-to-name path still works when the handler receives a payload with a missing or empty `resource.repository.id`
- fixed Azure DevOps PR reviews ending with a bot comment that says "no diff" even when the PR has real changes; `ReviewCommand.buildDiffs` was looking up unified-diff chunks by `diffs[i].Path` (e.g. `/README.md`, the leading slash that `gitforge`'s ADO `GetPullRequestFiles` returns) while `support.SplitUnifiedDiff` keys chunks by the bare new-side path (`README.md`, parsed from `diff --git a/X b/X`). The lookup missed for every ADO file, leaving the diff body empty under each `### File:` header and tricking the LLM into reporting "no diff to review". Centralised the normalisation in a new `support.LookupChunkByPath(chunks, path)` helper so the leading slash is stripped at exactly one site, with a regression test exercising both the bare and the leading-slash shapes

## [1.3.0] - 2026-04-28

### Added

- added `git.pullrequest.created` and `git.pullrequest.updated` handler that builds an `azuredevops` `ReviewProvider` and enqueues active PRs for asynchronous review
- added `Server.AllowedOrganizations` and `Server.AllowedProjects` allowlists (defense-in-depth) consulted by both webhook handlers, returning `403 Forbidden` for off-list payloads
- added a `Dockerfile` (multi-stage `golang:1.26-alpine` builder, `gcr.io/distroless/static-debian12:nonroot` runtime, `EXPOSE 8080`) and a `.dockerignore`
- added a `health` subcommand (`code-guru health`) that probes a running `serve` listener and exits `0` on `200`, `1` otherwise; used by the `Dockerfile` `HEALTHCHECK` directive (the distroless base image has no shell, no `curl`, no `wget`, so the binary doubles as its own healthcheck client)
- added a `HEALTHCHECK` directive to the `Dockerfile` calling `code-guru health` with a 30s interval and a 10s start period, so `docker run` / `compose` deployments get a live readiness signal without requiring a Kubernetes probe
- added a bounded asynchronous worker `Pool` (configurable `Workers` and `QueueSize`) that drains review jobs and recovers from per-job panics so a single failure does not crash the worker
- added Basic Auth verification for the Azure DevOps Service Hook endpoint (constant `code-guru` username, configurable secret password)
- added GitHub `pull_request` (`opened`, `synchronize`, `reopened`) handler with GitHub App installation token exchange (RS256 JWT, `sync.Map`-backed cache with a 5-minute safety margin) and a configured PAT fallback
- added graceful shutdown to the `serve` controller, capturing `SIGINT`/`SIGTERM` and draining both the HTTP server and the worker pool within `Server.ShutdownTimeout`
- added HMAC-SHA256 verification for the GitHub `pull_request` webhook endpoint via the `X-Hub-Signature-256` header

### Changed

- changed `Dispatcher.findToken` to fall back to a single untyped provider entry, so the env-only configuration (`CODE_GURU_PROVIDER_TOKEN`) works for both GitHub and Azure DevOps webhook handlers
- changed `NewSettings` to also resolve `${ENV_VAR}`/file-path references for `server.webhook_secret` and `github_app.private_key`, so YAML literals like `${CODE_GURU_WEBHOOK_SECRET}` are expanded before reaching the auth/JWT code paths
- changed `Pool.Submit` to hold the same mutex used by `Shutdown` while sending on the queue, eliminating the TOCTOU race that could panic with `send on closed channel` under concurrent traffic and graceful shutdown
- changed `Pool` workers to receive a cancellable base context that `Shutdown` cancels, so in-flight `JobHandler` invocations can observe shutdown timeouts via the `ctx` argument
- changed `ServeController.Execute` to validate required settings (`ai.backend`, `server.webhook_secret`) up front and exit fatally instead of starting with the empty `Settings` fallback
- changed `VerifyBasicAuth` to accept the `Basic` scheme prefix case-insensitively per RFC 7617/7235
- changed the `--port` flag on `serve` to be properly registered via `BindFlags` (it previously read but never declared the flag)
- changed the `Dockerfile` `RUN` shell to `/bin/bash` and added `set -euxo pipefail` so download-pipeline failures cannot be masked by `sh`/`dash` semantics
- changed the `Dockerfile` runtime stage from `gcr.io/distroless/static-debian12:nonroot` to `debian:12-slim@sha256:f9c6a2fd2ddbc23e336b6257a5245e31f996953ef06cd13a59fa0a1df2d5c252` so the `claude` AI backend can exec the Claude Code CLI inside the container; the native binary is installed via `claude.ai/install.sh stable` on every image rebuild (intentionally not version-pinned so security fixes ship without a manual bump; the `stable` channel is passed explicitly so the image is insulated from a future change to the installer default; downloaded to a file, then executed — no `curl | bash` pipe; the resolved version is written to `/etc/claude-version` for runtime traceability)
- changed the `serve` controller to register the dispatcher and itself in the DIG container so all subcommands ship with one binary
- changed the HTTP server's `ReadHeaderTimeout` to a dedicated `defaultReadHeaderTimeout` (`10s`) instead of reusing `defaultShutdownTimeout`
- refreshed `.github/copilot-instructions.md` to mark the webhook handlers as functional (no longer WIP)
- refreshed `.github/copilot-instructions.md` to remove the `anthropic-sdk-go` dependency that was replaced with direct HTTP calls in 1.2.5

### Fixed

- fixed `go.mod` to mark `github.com/golang-jwt/jwt/v5` as a direct dependency (it is imported directly by the installation token exchanger)
- fixed `installation_token_exchange.go` to handle `io.ReadAll` errors and to send a `User-Agent` header on the GitHub installation token exchange request, preventing silent body truncation and 403 rejections from GitHub

## [1.2.5] - 2026-04-24

### Changed

- changed the Anthropic backend to call the Messages API over HTTP directly instead of using `anthropic-sdk-go`
- changed the Go module dependencies to their latest versions

### Removed

- removed the `github.com/anthropics/anthropic-sdk-go` dependency

## [1.2.4] - 2026-04-19

### Changed

- changed the Go module dependencies to their latest versions

## [1.2.3] - 2026-04-17

### Changed

- changed the Go module dependencies to their latest versions

## [1.2.2] - 2026-04-16

### Changed

- changed the Go module dependencies to their latest versions

## [1.2.1] - 2026-04-15

### Changed

- changed the Go version to `1.26.2` and updated all module dependencies

### Fixed

- fixed `exhaustive` lint failure by adding `LanguageRuby` to the `languageToRuleCategory` map in `file_classifier.go` after the `langforge` upgrade introduced the new language constant

## [1.2.0] - 2026-04-14

### Added

- added automatic version check on CLI startup using `CheckForUpdates()`

### Changed

- changed the Go module dependencies to their latest versions

## [1.1.0] - 2026-04-03

### Added

- added `FlagBinder` optional interface for controllers to register command-specific flags
- added `self-update` subcommand to update the CLI binary from GitHub releases
- added `SelfUpdaterRepository` interface and `CliforgeSelfUpdaterRepository` implementation following Clean Architecture
- added `version` subcommand to display the current CLI version

### Changed

- changed cliforge import paths to reflect upstream `pkg/` restructuring
- changed the Go module dependencies to their latest versions

## [1.0.3] - 2026-03-31

### Changed

- changed the Go module dependencies to their latest versions

## [1.0.2] - 2026-03-30

### Changed

- changed the Go module dependencies to their latest versions

## [1.0.1] - 2026-03-24

### Changed

- changed the Go module dependencies to their latest versions

## [1.0.0] - 2026-03-23

### Added

- added `--version` flag to the CLI using Cobra's built-in version support
- added `.autobump.yaml` validation for bump-* trivial adapters to verify version files are present
- added `update-go`, `update-node`, `update-python` trivial adapters for dependency update PRs
- added version ldflags injection at build time via `make build` and `make install` targets

### Changed

- **BREAKING CHANGE:** changed `bump-go`, `bump-node`, `bump-python` trivial adapters to detect version bump (release ceremony) PRs instead of dependency updates; users who configured these for dependency updates must switch to `update-go`, `update-node`, `update-python`
- changed `TrivialDetector` interface to use `Detect(ctx, DetectionContext) DetectionResult` instead of `IsTrivial(files) bool` + `Summary(files) string`, enabling three-way verdicts (approve/reject/not-detected)
- changed `TrivialDetectorRegistry.Detect` to return a `DetectionResult` with verdict, enabling bump PR rejection when `.autobump.yaml` validation fails
- changed the Go module dependencies to their latest versions

## [0.2.1] - 2026-03-19

### Changed

- changed the Go module dependencies to their latest versions

## [0.2.0] - 2026-03-17

### Added

- added AI verdict system (`approve`, `request_changes`, `comment`) to review response for merge decisions
- added Anthropic API backend using the official Go SDK (`github.com/anthropics/anthropic-sdk-go`)
- added environment variable configuration fallback (`CODE_GURU_*`) for CI/CD environments
- added shared response parser (`support.ParseReviewResponse`) to eliminate duplicate parsing logic across backends
- added trivial PR detection that skips LLM when CI passes (CI status provided by webhook events; CLI auto-detection pending `gitforge` support)
- added trivial PR detection with built-in adapters (`bump-go`, `bump-node`, `bump-python`, `docs-only`) that skip the LLM

### Changed

- changed `ReviewCommand` to accept a `DetectorRegistry` for trivial PR detection
- changed `ReviewController` to fall back to environment variables when no config file is found
- changed `ReviewResult` entity to include a `Verdict` field for merge eligibility decisions
- changed AI system prompt to include verdict instructions and JSON schema

## [0.1.0] - 2026-03-12

### Added

- added `DiscoverCommand` in domain layer to separate business logic from controller
- added `end_line` and `suggestion` fields to `ReviewComment` for multi-line and code suggestion support
- added `SplitUnifiedDiff` utility for splitting multi-file diffs into per-file chunks
- added Claude Code CLI as an AI backend (alongside OpenAI) with configurable `max_turns`
- added diff fallback in review command for providers without per-file patches (e.g. Azure DevOps)
- added GitHub Actions workflow for CI/CD pipeline
- added glob-based rule matching for precise language/file filtering
- added unit tests for prompt builder, file classifier, URL parser, diff splitter, rules repository, and response parsing
- added YAML `frontmatter` stripping from rule files to extract `paths` globs

### Changed

- changed `DiscoverController` to delegate to `DiscoverCommand` following Clean Architecture
- changed Claude CLI backend to pass user prompt via stdin instead of CLI argument to avoid OS argument length limits
- changed Claude CLI response parsing to handle JSON wrapped in Markdown code fences
- changed OpenAI backend to enforce JSON response format via `ResponseFormat` parameter
- changed system prompt to include strict JSON-only instructions, line number rules, and severity definitions
- changed the Go version to `1.26.1` and updated all module dependencies
- replaced inline `parseGitHubURL` and `parseAzureDevOpsURL` PR URL parsing with `gitforge`'s `ParsePullRequestURL` to consolidate duplicated code
- replaced local `ProviderConfig` struct, `resolveToken()`, and `FindConfigFile()` with `gitforge`'s shared implementations
- replaced local file extension classifier with `langforge`'s `ClassifyFileByExtension` and `ClassifyFilesByExtension` to centralize language abstractions
- replaced raw struct literals in tests with `testkit` builders for consistent test data construction

### Fixed

- fixed `exhaustive` findings by adding missing `Language` and `ServiceType` keys to classifier and URL parser maps
