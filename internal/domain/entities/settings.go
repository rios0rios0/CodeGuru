package entities

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	configEntities "github.com/rios0rios0/gitforge/v4/pkg/config/domain/entities"
	"gopkg.in/yaml.v3"
)

// Settings is the top-level configuration for code-guru, loaded from YAML.
type Settings struct {
	Providers []ProviderConfig `yaml:"providers"`
	AI        AIConfig         `yaml:"ai"`
	Rules     RulesConfig      `yaml:"rules"`
	Trivial   TrivialConfig    `yaml:"trivial"`
	Server    ServerConfig     `yaml:"server"`
	GitHubApp GitHubAppConfig  `yaml:"github_app"`

	// BotIdentities lists the account identities code-guru posts review
	// comments under (a service-account login / email). It drives TWO
	// behaviours, so read both before adding an entry:
	//
	//  1. Self-recognition. On the re-review path the bot uses these to
	//     recognise its OWN prior comments so it can read the dialogue
	//     (its earlier findings plus the author's replies) and resolve
	//     each thread instead of re-posting the same findings; the
	//     webhook handlers also use them to skip comments the bot itself
	//     authored, which would otherwise loop forever. The built-in
	//     GitHub App shape (`code-guru[bot]`) is always recognised, and
	//     the bot self-detects its identity from the author of its own
	//     PR-wide status annotations — so this is only required when
	//     neither of those covers the deployment.
	//
	//  2. Mention triggers. Each entry is also normalised into the
	//     `@`-mentions that request a re-review (see
	//     `support.HasMention`), because users @-mention the account they
	//     see on the PR rather than the built-in `@code-guru` — which
	//     stays accepted regardless. `code-guru[bot]` accepts
	//     `@code-guru`; `svc-codeguru@corp.example` accepts
	//     `@svc-codeguru`; an Azure DevOps identity GUID accepts the
	//     `@<guid>` markup the ADO comment box substitutes for an
	//     @-autocompleted mention.
	//
	// Because of (2), list ONLY accounts this bot posts under. A shared
	// automation account added here to quieten (1) becomes a live
	// re-review trigger: anyone typing its name in a PR comment starts a
	// full review. Use `Trivial.AutoMergeAllowedAuthors` for third-party
	// automation accounts instead.
	//
	// Honours `CODE_GURU_BOT_IDENTITIES` (comma-separated).
	BotIdentities []string `yaml:"bot_identities"`
}

// ProviderConfig is an alias for gitforge's ProviderConfig to maintain backward compatibility.
type ProviderConfig = configEntities.ProviderConfig

// AIConfig holds settings for the AI review backend.
type AIConfig struct {
	Backend   string          `yaml:"backend"`
	OpenAI    OpenAIConfig    `yaml:"openai"`
	Claude    ClaudeConfig    `yaml:"claude"`
	Anthropic AnthropicConfig `yaml:"anthropic"`

	// SubmitNativeReview, when set, controls whether the bot also records
	// a native pull request review (Approved / Changes Requested) on
	// GitHub or Azure DevOps. Comment-only verdicts are not submitted as
	// native reviews (support.MapVerdictToReview returns ok=false for
	// them). The native review surfaces the verdict in the platform's
	// reviewer panel.
	//
	// Tri-state pointer so YAML / env "unset" can mean "use the default":
	// nil resolves to true via NativeReviewSubmissionEnabled (default ON).
	// Operators that want to opt out explicitly set
	// `submit_native_review: false` in YAML or
	// CODE_GURU_AI_SUBMIT_NATIVE_REVIEW=false. Call sites should always
	// read the resolved value via NativeReviewSubmissionEnabled rather
	// than dereferencing the pointer directly.
	SubmitNativeReview *bool `yaml:"submit_native_review"`

	// ReviewDrafts, when true, lets the bot review draft PRs as well. By
	// default draft PRs are skipped — most teams treat drafts as
	// work-in-progress that should not consume review budget. Override via
	// CODE_GURU_AI_REVIEW_DRAFTS=true.
	ReviewDrafts bool `yaml:"review_drafts"`

	// MaxAttempts is the total number of times the AI backend is invoked
	// per review before giving up (1 = no retry). LLM output is non-
	// deterministic, so a re-sample usually turns a non-JSON or transient-
	// error response (e.g. the claude CLI's dropped-socket error) into a
	// clean review — retrying avoids a "review failed" annotation on the PR
	// for what is almost always a recoverable blip. Resolve via
	// ReviewAttempts() (defaults to 3 when unset). Honours
	// CODE_GURU_AI_MAX_ATTEMPTS.
	MaxAttempts int `yaml:"max_attempts"`

	// ProjectGuidelines, when set, controls whether the bot loads the
	// reviewed repository's own root `CLAUDE.md` and forwards it to the
	// LLM as project-specific review context, so the review honours the
	// project's own conventions in addition to the operator-configured
	// rules. Works on every provider that supports API file access
	// (GitHub, Azure DevOps); the fetch is best-effort — a missing file
	// or a provider error never fails the review.
	//
	// Tri-state pointer so YAML / env "unset" can mean "use the default":
	// nil resolves to true via ProjectGuidelinesEnabled (default ON).
	// Operators that want to opt out explicitly set
	// `project_guidelines: false` in YAML or
	// CODE_GURU_AI_PROJECT_GUIDELINES=false. Call sites should always
	// read the resolved value via ProjectGuidelinesEnabled rather than
	// dereferencing the pointer directly.
	ProjectGuidelines *bool `yaml:"project_guidelines"`

	// PRMetadata, when set, controls whether the bot fetches the pull
	// request's author-supplied metadata (description and commit count)
	// from the provider and forwards it to the LLM as intent context —
	// so the model can judge whether the diff actually does what the
	// title, branch name, and description claim, and flag undocumented
	// scope creep. The fetch is best-effort: an unsupported provider or
	// a fetch error never fails the review.
	//
	// Tri-state pointer so YAML / env "unset" can mean "use the default":
	// nil resolves to true via PullRequestMetadataEnabled (default ON).
	// Operators that want to opt out explicitly set
	// `pr_metadata: false` in YAML or CODE_GURU_AI_PR_METADATA=false.
	// Call sites should always read the resolved value via
	// PullRequestMetadataEnabled rather than dereferencing the pointer
	// directly.
	PRMetadata *bool `yaml:"pr_metadata"`

	// MaxGuidelinesBytes bounds how much of the reviewed repository's own
	// `CLAUDE.md` is forwarded to the LLM. Resolve via GuidelinesBytes()
	// (defaults to defaultMaxGuidelinesBytes when unset or non-positive).
	// Honours CODE_GURU_AI_MAX_GUIDELINES_BYTES.
	//
	// Lower this when the configured backend has a SMALL context window
	// (see the defaultMaxGuidelinesBytes doc): the default is sized for a
	// 1M-token window and a genuinely huge guidelines file could otherwise
	// crowd out the diff on a 128K/200K-token model.
	MaxGuidelinesBytes int `yaml:"max_guidelines_bytes"`

	// MaxPRDescriptionBytes bounds how much of the pull request's
	// description is forwarded to the LLM. Resolve via
	// PRDescriptionBytes() (defaults to defaultMaxPRDescriptionBytes when
	// unset or non-positive). Honours
	// CODE_GURU_AI_MAX_PR_DESCRIPTION_BYTES.
	MaxPRDescriptionBytes int `yaml:"max_pr_description_bytes"`

	// BatchLargeReviews, when set, controls whether a pull request that
	// does not fit the model's context window is reviewed in BATCHES —
	// its files split into chunks that fit, reviewed one after another,
	// and merged into a single review — instead of being abandoned with
	// a "too large, split your PR" notice. The batched run costs one
	// model call per batch and takes proportionally longer, which is why
	// it announces itself on the pull request.
	//
	// Tri-state pointer so YAML / env "unset" can mean "use the default":
	// nil resolves to true via BatchLargeReviewsEnabled (default ON).
	// Operators that want the old give-up behaviour — typically to cap
	// spend on a paid backend — explicitly set
	// `batch_large_reviews: false` in YAML or
	// CODE_GURU_AI_BATCH_LARGE_REVIEWS=false. Call sites should always
	// read the resolved value via BatchLargeReviewsEnabled rather than
	// dereferencing the pointer directly.
	BatchLargeReviews *bool `yaml:"batch_large_reviews"`

	// MaxReviewBatches caps how many batches a single batched review may
	// consume. Resolve via ReviewBatches() (defaults to
	// defaultMaxReviewBatches when unset or non-positive). Honours
	// CODE_GURU_AI_MAX_REVIEW_BATCHES.
	MaxReviewBatches int `yaml:"max_review_batches"`
}

// defaultReviewAttempts is the attempt budget applied when AI.MaxAttempts is
// unset or non-positive. 3 (one initial call plus two retries) clears the
// overwhelming majority of the transient / non-JSON failures observed in
// production while bounding worst-case latency to roughly 3x a single review.
const defaultReviewAttempts = 3

// ReviewAttempts resolves the per-review AI attempt budget. An unset or
// non-positive MaxAttempts falls back to defaultReviewAttempts so existing
// deployments pick up retries automatically; an explicit value (e.g.
// `max_attempts: 1` to disable retries, or a higher value for a flaky
// backend) wins.
func (a AIConfig) ReviewAttempts() int {
	if a.MaxAttempts <= 0 {
		return defaultReviewAttempts
	}
	return a.MaxAttempts
}

// defaultMaxReviewBatches is the batch cap applied when
// AI.MaxReviewBatches is unset or non-positive.
//
// 20 sequential model calls is a deliberately generous ceiling: it covers a
// ~4 MB diff even on a 128K-token model (the smallest window any supported
// backend runs), while still bounding a pathological change — a vendored
// dependency drop, an accidental `node_modules` commit — to a review that
// finishes rather than one that runs for hours and costs proportionally.
// Files still queued when the cap is reached are reported as unreviewed in
// the merged summary, so the author is never told a truncated review was
// complete.
const defaultMaxReviewBatches = 20

// ReviewBatches resolves the per-review batch cap. An unset or non-positive
// MaxReviewBatches falls back to defaultMaxReviewBatches; an explicit value
// wins (raise it for very large repositories, lower it to cap spend).
func (a AIConfig) ReviewBatches() int {
	if a.MaxReviewBatches <= 0 {
		return defaultMaxReviewBatches
	}
	return a.MaxReviewBatches
}

// BatchLargeReviewsEnabled resolves the tri-state BatchLargeReviews pointer
// into a single boolean. nil (the YAML / env "unset" state) returns true so
// deployments that never wire the flag review oversized pull requests in
// batches automatically instead of skipping them; an explicit
// `batch_large_reviews: false` in YAML or
// `CODE_GURU_AI_BATCH_LARGE_REVIEWS=false` returns false, restoring the
// "post a too-large notice and review nothing" behaviour. Callers should
// always go through this helper rather than dereferencing the pointer.
func (a AIConfig) BatchLargeReviewsEnabled() bool {
	if a.BatchLargeReviews == nil {
		return true
	}
	return *a.BatchLargeReviews
}

// defaultMaxGuidelinesBytes is the guidelines budget applied when
// AI.MaxGuidelinesBytes is unset or non-positive. 1 MiB is roughly 256k
// tokens at ~4 bytes/token, i.e. about a QUARTER of a 1M-token context
// window, leaving the remaining ~75% for the diff and the rest of the
// prompt.
//
// It is a ceiling, not an allocation: real `CLAUDE.md` files are tens of
// kilobytes, so for almost every repository this bound never binds and the
// whole document is sent. The previous 32 KiB ceiling was sized for a 200K
// window and silently truncated large, well-maintained guidelines files
// mid-document — the model then judged the diff against a partial copy of
// the project's conventions, which is worse than a long prompt.
//
// SMALL-WINDOW BACKENDS: this default assumes a 1M-token window (Anthropic
// with `ai.anthropic.context_1m`, on by default, or the Claude CLI).
// Operators on a 128K-token model (e.g. OpenAI `gpt-4o`) or on Anthropic
// with the 1M beta disabled (200K) should lower `ai.max_guidelines_bytes`
// accordingly — a pathologically large guidelines file could otherwise
// consume the window the diff needs.
const defaultMaxGuidelinesBytes = 1024 * 1024

// GuidelinesBytes resolves the project-guidelines byte budget. An unset or
// non-positive MaxGuidelinesBytes falls back to defaultMaxGuidelinesBytes so
// existing deployments pick up the larger budget automatically; an explicit
// value wins (lower it for small-window backends).
func (a AIConfig) GuidelinesBytes() int {
	if a.MaxGuidelinesBytes <= 0 {
		return defaultMaxGuidelinesBytes
	}
	return a.MaxGuidelinesBytes
}

// defaultMaxPRDescriptionBytes is the description budget applied when
// AI.MaxPRDescriptionBytes is unset or non-positive. 64 KiB is roughly 16k
// tokens — four times the previous 16 KiB ceiling, so a thorough
// hand-written description survives intact, while still refusing to spend a
// meaningful share of the window on the release-bot descriptions that paste
// entire upstream changelogs into the body. Sized deliberately below the
// guidelines budget: a description is intent context, whereas the
// guidelines are the standard the diff is judged against.
const defaultMaxPRDescriptionBytes = 64 * 1024

// PRDescriptionBytes resolves the PR-description byte budget. An unset or
// non-positive MaxPRDescriptionBytes falls back to
// defaultMaxPRDescriptionBytes; an explicit value wins.
func (a AIConfig) PRDescriptionBytes() int {
	if a.MaxPRDescriptionBytes <= 0 {
		return defaultMaxPRDescriptionBytes
	}
	return a.MaxPRDescriptionBytes
}

// NativeReviewSubmissionEnabled resolves the tri-state SubmitNativeReview
// pointer into a single boolean. nil (the YAML / env "unset" state) returns
// true so deployments that never wire the flag pick up the new default
// behaviour automatically; an explicit `submit_native_review: false` in YAML
// or `CODE_GURU_AI_SUBMIT_NATIVE_REVIEW=false` returns false. Callers should
// always go through this helper rather than dereferencing the pointer.
func (a AIConfig) NativeReviewSubmissionEnabled() bool {
	if a.SubmitNativeReview == nil {
		return true
	}
	return *a.SubmitNativeReview
}

// ProjectGuidelinesEnabled resolves the tri-state ProjectGuidelines pointer
// into a single boolean. nil (the YAML / env "unset" state) returns true so
// deployments that never wire the flag pick up the reviewed repository's
// CLAUDE.md automatically; an explicit `project_guidelines: false` in YAML
// or `CODE_GURU_AI_PROJECT_GUIDELINES=false` returns false. Callers should
// always go through this helper rather than dereferencing the pointer.
func (a AIConfig) ProjectGuidelinesEnabled() bool {
	if a.ProjectGuidelines == nil {
		return true
	}
	return *a.ProjectGuidelines
}

// PullRequestMetadataEnabled resolves the tri-state PRMetadata pointer into a
// single boolean. nil (the YAML / env "unset" state) returns true so
// deployments that never wire the flag pick up the PR description / commit
// count context automatically; an explicit `pr_metadata: false` in YAML or
// `CODE_GURU_AI_PR_METADATA=false` returns false. Callers should always go
// through this helper rather than dereferencing the pointer.
func (a AIConfig) PullRequestMetadataEnabled() bool {
	if a.PRMetadata == nil {
		return true
	}
	return *a.PRMetadata
}

// OpenAIConfig holds OpenAI-specific settings.
type OpenAIConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

// ClaudeConfig holds Claude CLI-specific settings.
type ClaudeConfig struct {
	BinaryPath string `yaml:"binary_path"`
	Model      string `yaml:"model"`
	MaxTurns   int    `yaml:"max_turns"`
}

// AnthropicConfig holds Anthropic API-specific settings.
type AnthropicConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`

	// Context1M, when set, controls whether the Anthropic backend requests
	// the 1M-token context window (the `context-1m-2025-08-07` beta) instead
	// of the default 200K. Enabling it lets pull requests up to ~5x larger be
	// reviewed in a single pass before they overflow the window and fail with
	// a "too large" annotation. For prompts under 200K tokens the beta is a
	// no-op, so it is safe on small PRs; very large prompts (200K–1M tokens)
	// may incur Anthropic long-context pricing, which is why it is toggleable.
	//
	// Tri-state pointer so YAML / env "unset" can mean "use the default":
	// nil resolves to true via Context1MEnabled (default ON). Operators whose
	// account or model does not support the beta can opt out with
	// `context_1m: false` in YAML or CODE_GURU_ANTHROPIC_CONTEXT_1M=false.
	// Call sites should always read the resolved value via Context1MEnabled
	// rather than dereferencing the pointer directly.
	Context1M *bool `yaml:"context_1m"`

	// RefusalFallbackModel, when set, is the Anthropic model the backend
	// re-issues the review against if the primary model declines the content
	// on content-safety grounds (`stop_reason: "refusal"`, common for
	// security-related code). Safety-classifier coverage varies by model, so a
	// fallback to a different model can produce a review where the primary
	// refused. Empty (the default) disables the fallback: a refusal is reported
	// as-is with the "content-safety declined" annotation. Honours
	// CODE_GURU_ANTHROPIC_REFUSAL_FALLBACK_MODEL.
	RefusalFallbackModel string `yaml:"refusal_fallback_model"`
}

// Context1MEnabled resolves the tri-state Context1M pointer into a single
// boolean. nil (the YAML / env "unset" state) returns true so deployments that
// never wire the flag pick up the larger context window automatically; an
// explicit `context_1m: false` in YAML or `CODE_GURU_ANTHROPIC_CONTEXT_1M=false`
// returns false. Callers should always go through this helper rather than
// dereferencing the pointer.
func (c AnthropicConfig) Context1MEnabled() bool {
	if c.Context1M == nil {
		return true
	}

	return *c.Context1M
}

// RulesConfig configures where review rules are loaded from.
type RulesConfig struct {
	Path       string   `yaml:"path"`
	Categories []string `yaml:"categories"`
}

// TrivialConfig configures trivial PR detection.
type TrivialConfig struct {
	Enabled  bool     `yaml:"enabled"`
	Adapters []string `yaml:"adapters"`
	// AutoMerge, when true, calls the provider's merge endpoint after a
	// trivial-approve verdict. Off by default — this bypasses human
	// review and merges cross-system, so the gate is "operator must
	// explicitly opt in". Honours `CODE_GURU_TRIVIAL_AUTO_MERGE`.
	AutoMerge bool `yaml:"auto_merge"`
	// MergeStrategy is the gitforge merge strategy applied when
	// AutoMerge fires (`"merge"` / `"squash"` / `"rebase"`). Empty
	// falls back to the platform default. Honours
	// `CODE_GURU_TRIVIAL_MERGE_STRATEGY`.
	MergeStrategy string `yaml:"merge_strategy"`
	// BypassPolicy, when true, asks the provider to skip branch
	// policies (`Required reviewers`, `Minimum approver count`, etc.)
	// when AutoMerge fires. Off by default — bypass strictly requires
	// the bot's identity to hold the platform-level
	// `Bypass policies when completing pull requests` permission, so
	// turning this on without that permission turns previously-working
	// auto-merges into hard 403s. Operators in environments where the
	// bot has merge permission but NOT bypass permission should leave
	// this off and let `Required reviewers` policies remain
	// authoritative. Honours `CODE_GURU_TRIVIAL_BYPASS_POLICIES`.
	BypassPolicy bool `yaml:"bypass_policy"`
	// AutoMergeAllowedAuthors restricts auto-merge to PRs opened by one of
	// the listed account identities (e.g. the autobump / autoupdate /
	// config-automation service account). Triviality decides whether a PR
	// is eligible to auto-merge; this allowlist decides whether its author
	// is trusted to merge unattended. When non-empty, only matching authors
	// auto-merge — a human's docs PR is approved but left for a human to
	// merge. When empty, auto-merge falls back to "any author" for
	// backward compatibility (not recommended with BypassPolicy, which then
	// force-merges every trivial PR past `Required reviewers`). Honours
	// `CODE_GURU_TRIVIAL_AUTO_MERGE_AUTHORS` (comma-separated).
	AutoMergeAllowedAuthors []string `yaml:"auto_merge_allowed_authors"`
	// DeleteSourceBranch, when set, controls whether an auto-merged trivial
	// PR also has its source branch deleted once the merge completes (gitforge's
	// `WithDeleteSourceBranch` merge option). Tri-state: nil resolves to
	// true via DeleteSourceBranchEnabled (default ON) — the common desire is a
	// clean branch list after the bot merges an automation PR. Only takes
	// effect when AutoMerge actually fires; without a merge there is no branch
	// to delete. Set `delete_source_branch: false` in YAML or
	// CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH=false to keep the branch. Branch
	// deletion is best-effort in gitforge: a failure never fails the merge.
	DeleteSourceBranch *bool `yaml:"delete_source_branch"`
}

// DeleteSourceBranchEnabled resolves the tri-state DeleteSourceBranch pointer
// into a plain bool: an unset value (nil) defaults to true, so a trivial
// auto-merge deletes the source branch unless an operator explicitly opts out
// with `delete_source_branch: false` or CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH=false.
// Callers should always go through this method rather than dereferencing the
// pointer so the default is applied consistently.
func (t TrivialConfig) DeleteSourceBranchEnabled() bool {
	if t.DeleteSourceBranch == nil {
		return true
	}
	return *t.DeleteSourceBranch
}

// ServerConfig holds settings for the webhook server.
type ServerConfig struct {
	Port                 int           `yaml:"port"`
	WebhookSecret        string        `yaml:"webhook_secret"`
	QueueSize            int           `yaml:"queue_size"`
	Workers              int           `yaml:"workers"`
	ShutdownTimeout      time.Duration `yaml:"shutdown_timeout"`
	AllowedOrganizations []string      `yaml:"allowed_organizations"`
	AllowedProjects      []string      `yaml:"allowed_projects"`
	AllowedSourceCIDRs   []string      `yaml:"allowed_source_cidrs"`
}

// GitHubAppConfig holds GitHub App authentication settings.
type GitHubAppConfig struct {
	AppID      int64  `yaml:"app_id"`
	PrivateKey string `yaml:"private_key"`
}

// NewSettings reads and parses a configuration file, expanding environment variables.
func NewSettings(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %q: %w", path, err)
	}

	var settings Settings
	if unmarshalErr := yaml.Unmarshal(data, &settings); unmarshalErr != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", unmarshalErr)
	}

	for i := range settings.Providers {
		settings.Providers[i].Token = settings.Providers[i].ResolveToken()
	}
	settings.AI.OpenAI.APIKey = configEntities.ResolveToken(settings.AI.OpenAI.APIKey)
	settings.AI.Anthropic.APIKey = configEntities.ResolveToken(settings.AI.Anthropic.APIKey)
	// Webhook server fields support the same ${ENV_VAR}/file-path expansion as
	// provider tokens. Resolve them here so the serve command can read literals
	// like "${CODE_GURU_WEBHOOK_SECRET}" from YAML and have them expanded before
	// reaching the auth/JWT code paths.
	settings.Server.WebhookSecret = configEntities.ResolveToken(settings.Server.WebhookSecret)
	settings.GitHubApp.PrivateKey = configEntities.ResolveToken(settings.GitHubApp.PrivateKey)

	// Env vars take precedence over YAML for the small set of fields
	// where deployments commonly use a config file as a baseline and
	// override per-environment via env. Expand with discipline —
	// every override here erodes the "config file is authoritative"
	// guarantee, so only add fields that genuinely need it.
	if envAdapters := parseTrivialAdaptersEnv(); len(envAdapters) > 0 {
		settings.Trivial.Enabled = true
		settings.Trivial.Adapters = envAdapters
	}
	if raw := strings.TrimSpace(os.Getenv("CODE_GURU_TRIVIAL_AUTO_MERGE")); raw != "" {
		if v, parseErr := strconv.ParseBool(raw); parseErr == nil {
			settings.Trivial.AutoMerge = v
		}
	}
	if raw := strings.TrimSpace(os.Getenv("CODE_GURU_TRIVIAL_MERGE_STRATEGY")); raw != "" {
		settings.Trivial.MergeStrategy = raw
	}
	if raw := strings.TrimSpace(os.Getenv("CODE_GURU_TRIVIAL_BYPASS_POLICIES")); raw != "" {
		if v, parseErr := strconv.ParseBool(raw); parseErr == nil {
			settings.Trivial.BypassPolicy = v
		}
	}
	if authors := splitCSV(os.Getenv("CODE_GURU_TRIVIAL_AUTO_MERGE_AUTHORS")); len(authors) > 0 {
		settings.Trivial.AutoMergeAllowedAuthors = authors
	}
	// Tri-state kill switch for the default-ON source-branch deletion: only an
	// explicit CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH overrides the YAML value,
	// so an unset env var leaves whatever the config file said (or nil = ON).
	if v := parseOptionalBoolEnv("CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH"); v != nil {
		settings.Trivial.DeleteSourceBranch = v
	}
	if ids := splitCSV(os.Getenv("CODE_GURU_BOT_IDENTITIES")); len(ids) > 0 {
		settings.BotIdentities = ids
	}
	applyNumericAIEnvOverrides(&settings.AI)
	// Kill switches for the default-ON AI features. Deployments commonly ship a
	// YAML baseline and flip per-environment behaviour via env (the same
	// argument as CODE_GURU_AI_MAX_ATTEMPTS above); without these overrides an
	// operator's opt-out on a YAML-configured pod would be silently ignored.
	applyTristateAIEnvOverrides(&settings.AI)

	if validateErr := validateSettings(&settings); validateErr != nil {
		return nil, validateErr
	}

	return &settings, nil
}

// applyNumericAIEnvOverrides applies the integer AI env overrides onto an
// AIConfig already populated from YAML: the per-review attempt budget and the
// two prompt budgets. A deployment commonly ships a YAML baseline and tunes
// these per environment — a small-context-window backend, for instance, must
// be able to lower the guidelines budget without re-rendering its YAML.
//
// Each override fires only when the value parses to a POSITIVE integer, so a
// typo'd or empty variable leaves the YAML value untouched rather than zeroing
// the budget (which the resolvers would then read as "unset" anyway, but the
// explicit guard keeps the YAML value intact for round-tripping).
//
// Extracted from NewSettings so that function stays within the
// cognitive-complexity budget as the override list grows, mirroring
// applyTristateAIEnvOverrides below.
func applyNumericAIEnvOverrides(ai *AIConfig) {
	overrides := map[string]*int{
		"CODE_GURU_AI_MAX_ATTEMPTS":             &ai.MaxAttempts,
		"CODE_GURU_AI_MAX_GUIDELINES_BYTES":     &ai.MaxGuidelinesBytes,
		"CODE_GURU_AI_MAX_PR_DESCRIPTION_BYTES": &ai.MaxPRDescriptionBytes,
		"CODE_GURU_AI_MAX_REVIEW_BATCHES":       &ai.MaxReviewBatches,
	}
	for envVar, target := range overrides {
		raw := strings.TrimSpace(os.Getenv(envVar))
		if raw == "" {
			continue
		}
		if v, parseErr := strconv.Atoi(raw); parseErr == nil && v > 0 {
			*target = v
		}
	}
}

// applyTristateAIEnvOverrides applies the env kill switches for the default-ON
// tri-state AI features onto an AIConfig already populated from YAML. Each
// override fires only when the env var parses to a bool; nil (unset or
// unparseable) leaves the YAML value untouched. Extracted from NewSettings so
// that function stays within the cognitive-complexity budget as the tri-state
// list grows.
func applyTristateAIEnvOverrides(ai *AIConfig) {
	if v := parseOptionalBoolEnv("CODE_GURU_AI_PROJECT_GUIDELINES"); v != nil {
		ai.ProjectGuidelines = v
	}
	if v := parseOptionalBoolEnv("CODE_GURU_AI_PR_METADATA"); v != nil {
		ai.PRMetadata = v
	}
	if v := parseOptionalBoolEnv("CODE_GURU_AI_BATCH_LARGE_REVIEWS"); v != nil {
		ai.BatchLargeReviews = v
	}
	if v := parseOptionalBoolEnv("CODE_GURU_ANTHROPIC_CONTEXT_1M"); v != nil {
		ai.Anthropic.Context1M = v
	}
}

// parseTrivialAdaptersEnv parses `CODE_GURU_TRIVIAL_ADAPTERS` into a
// trimmed slice of adapter names. Returns nil when unset or empty.
// Lives at the package level so `NewSettings` (YAML path) and
// `NewSettingsFromEnv` (env-only path) share the same parsing.
func parseTrivialAdaptersEnv() []string {
	raw := os.Getenv("CODE_GURU_TRIVIAL_ADAPTERS")
	if raw == "" {
		return nil
	}
	var adapters []string
	for a := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(a); trimmed != "" {
			adapters = append(adapters, trimmed)
		}
	}
	return adapters
}

// NewSettingsFromEnv builds settings entirely from environment variables.
func NewSettingsFromEnv() (*Settings, error) {
	maxTurns, _ := strconv.Atoi(envOrDefault("CODE_GURU_CLAUDE_MAX_TURNS", "1"))
	maxAttempts, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("CODE_GURU_AI_MAX_ATTEMPTS")))
	maxGuidelines, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("CODE_GURU_AI_MAX_GUIDELINES_BYTES")))
	maxPRDescription, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("CODE_GURU_AI_MAX_PR_DESCRIPTION_BYTES")))
	maxReviewBatches, _ := strconv.Atoi(strings.TrimSpace(os.Getenv("CODE_GURU_AI_MAX_REVIEW_BATCHES")))
	port, _ := strconv.Atoi(envOrDefault("CODE_GURU_PORT", "8080"))
	appID, _ := strconv.ParseInt(os.Getenv("CODE_GURU_GITHUB_APP_ID"), 10, 64)
	queueSize, _ := strconv.Atoi(envOrDefault("CODE_GURU_SERVER_QUEUE_SIZE", "100"))
	workers, _ := strconv.Atoi(envOrDefault("CODE_GURU_SERVER_WORKERS", strconv.Itoa(runtime.NumCPU())))
	shutdownTimeout, _ := time.ParseDuration(envOrDefault("CODE_GURU_SERVER_SHUTDOWN_TIMEOUT", "30s"))

	adapters := parseTrivialAdaptersEnv()

	settings := &Settings{
		AI: AIConfig{
			Backend: envOrDefault("CODE_GURU_BACKEND", "openai"),
			OpenAI: OpenAIConfig{
				APIKey: os.Getenv("CODE_GURU_OPENAI_API_KEY"),
				Model:  envOrDefault("CODE_GURU_OPENAI_MODEL", "gpt-4o"),
			},
			Claude: ClaudeConfig{
				BinaryPath: envOrDefault("CODE_GURU_CLAUDE_BINARY_PATH", "claude"),
				Model:      envOrDefault("CODE_GURU_CLAUDE_MODEL", "sonnet"),
				MaxTurns:   maxTurns,
			},
			Anthropic: AnthropicConfig{
				APIKey:               os.Getenv("CODE_GURU_ANTHROPIC_API_KEY"),
				Model:                envOrDefault("CODE_GURU_ANTHROPIC_MODEL", "claude-sonnet-4-20250514"),
				Context1M:            parseOptionalBoolEnv("CODE_GURU_ANTHROPIC_CONTEXT_1M"),
				RefusalFallbackModel: os.Getenv("CODE_GURU_ANTHROPIC_REFUSAL_FALLBACK_MODEL"),
			},
			SubmitNativeReview:    parseOptionalBoolEnv("CODE_GURU_AI_SUBMIT_NATIVE_REVIEW"),
			ReviewDrafts:          parseBoolEnv("CODE_GURU_AI_REVIEW_DRAFTS", false),
			MaxAttempts:           maxAttempts,
			ProjectGuidelines:     parseOptionalBoolEnv("CODE_GURU_AI_PROJECT_GUIDELINES"),
			PRMetadata:            parseOptionalBoolEnv("CODE_GURU_AI_PR_METADATA"),
			MaxGuidelinesBytes:    maxGuidelines,
			MaxPRDescriptionBytes: maxPRDescription,
			BatchLargeReviews:     parseOptionalBoolEnv("CODE_GURU_AI_BATCH_LARGE_REVIEWS"),
			MaxReviewBatches:      maxReviewBatches,
		},
		Rules: RulesConfig{
			Path: os.Getenv("CODE_GURU_RULES_PATH"),
		},
		Trivial: TrivialConfig{
			Enabled:                 len(adapters) > 0,
			Adapters:                adapters,
			AutoMerge:               parseBoolEnv("CODE_GURU_TRIVIAL_AUTO_MERGE", false),
			MergeStrategy:           strings.TrimSpace(os.Getenv("CODE_GURU_TRIVIAL_MERGE_STRATEGY")),
			BypassPolicy:            parseBoolEnv("CODE_GURU_TRIVIAL_BYPASS_POLICIES", false),
			AutoMergeAllowedAuthors: splitCSV(os.Getenv("CODE_GURU_TRIVIAL_AUTO_MERGE_AUTHORS")),
			DeleteSourceBranch:      parseOptionalBoolEnv("CODE_GURU_TRIVIAL_DELETE_SOURCE_BRANCH"),
		},
		Server: ServerConfig{
			Port:                 port,
			WebhookSecret:        os.Getenv("CODE_GURU_WEBHOOK_SECRET"),
			QueueSize:            queueSize,
			Workers:              workers,
			ShutdownTimeout:      shutdownTimeout,
			AllowedOrganizations: splitCSV(os.Getenv("CODE_GURU_SERVER_ALLOWED_ORGANIZATIONS")),
			AllowedProjects:      splitCSV(os.Getenv("CODE_GURU_SERVER_ALLOWED_PROJECTS")),
			AllowedSourceCIDRs:   splitCSV(os.Getenv("CODE_GURU_SERVER_ALLOWED_SOURCE_CIDRS")),
		},
		GitHubApp: GitHubAppConfig{
			AppID:      appID,
			PrivateKey: os.Getenv("CODE_GURU_GITHUB_PRIVATE_KEY"),
		},
		BotIdentities: splitCSV(os.Getenv("CODE_GURU_BOT_IDENTITIES")),
	}

	if token := os.Getenv("CODE_GURU_PROVIDER_TOKEN"); token != "" {
		settings.Providers = []ProviderConfig{{Token: token}}
	}

	if err := validateSettings(settings); err != nil {
		return nil, err
	}

	return settings, nil
}

func validateSettings(settings *Settings) error {
	if settings.AI.Backend == "" {
		return errors.New("ai.backend is required (openai, claude, or anthropic)")
	}

	validBackends := map[string]bool{
		"openai":    true,
		"claude":    true,
		"anthropic": true,
	}
	if !validBackends[settings.AI.Backend] {
		return fmt.Errorf("ai.backend %q is not supported (valid: openai, claude, anthropic)", settings.AI.Backend)
	}

	if settings.AI.Backend == "openai" && settings.AI.OpenAI.APIKey == "" {
		return errors.New("ai.openai.api_key is required when backend is openai")
	}

	if settings.AI.Backend == "anthropic" && settings.AI.Anthropic.APIKey == "" {
		return errors.New("ai.anthropic.api_key is required when backend is anthropic")
	}

	return nil
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseBoolEnv reads a boolean environment variable. Truthy values are the
// strconv defaults (`1`, `t`, `true` — case-insensitive); any non-empty value
// the parser rejects falls back to the provided default rather than panicking,
// so a typo does not silently flip behaviour. Surrounding whitespace is
// trimmed before parsing so values shipped via Helm/templating (which often
// leave a trailing newline or space, e.g. `"false "`) parse correctly.
func parseBoolEnv(key string, fallback bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

// parseOptionalBoolEnv reads a tri-state boolean env var. An unset variable
// (or one that is whitespace-only after trimming) returns nil so downstream
// resolvers (e.g. NativeReviewSubmissionEnabled) can apply their default. A
// set-but-unparseable value also returns nil so a typo does not silently
// flip behaviour — operators see the default instead. Truthy values follow
// the [strconv.ParseBool] defaults. Surrounding whitespace is trimmed before
// parsing so Helm-rendered values like `"false "` survive the round-trip and
// the operator's explicit opt-out is honoured (without trimming, ParseBool
// would reject the trailing space and the resolver would fall back to the
// default ON, which is exactly the silent flip this branch tries to avoid).
func parseOptionalBoolEnv(key string) *bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parsed, err := strconv.ParseBool(raw)
	if err != nil {
		return nil
	}
	return &parsed
}

// splitCSV parses a comma-separated string into a slice, trimming whitespace and skipping empties.
func splitCSV(raw string) []string {
	if raw == "" {
		return nil
	}
	var out []string
	for v := range strings.SplitSeq(raw, ",") {
		if trimmed := strings.TrimSpace(v); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
