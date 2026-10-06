package entities

import forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

// FileDiff represents a single file change within a pull request.
type FileDiff struct {
	Path     string
	Diff     string
	Language string
}

// ReviewComment represents a single review comment to post on a PR.
type ReviewComment struct {
	FilePath   string `json:"file"`
	Line       int    `json:"line"`
	EndLine    int    `json:"end_line,omitempty"`
	Body       string `json:"body"`
	Severity   string `json:"severity"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ReviewResult aggregates all review output for a single PR.
type ReviewResult struct {
	PullRequestURL string          `json:"pull_request_url,omitempty"`
	Verdict        string          `json:"verdict,omitempty"`
	Comments       []ReviewComment `json:"comments"`
	Summary        string          `json:"summary"`

	// ThreadResolutions carries the AI's per-thread verdict on every
	// prior bot review thread that was surfaced in the conversation
	// block of the user prompt. Populated only on the mention re-review
	// path (`ReviewOptions.UserMentioned == true`); empty on first-pass
	// reviews where there are no prior threads to classify.
	//
	// Each entry tells the post-pipeline what to do with the existing
	// thread: `resolved` triggers a thread-status update + a short
	// confirmation reply, `outstanding` posts a restated reply, and
	// `outdated` is a soft "no longer applicable" close. The shape lets
	// the LLM make a structured decision instead of re-emitting (or
	// silently dropping) findings via `Comments`, which is the failure
	// mode that drove this PR — every re-review used to flood the PR
	// with reworded duplicates of the same comment.
	ThreadResolutions []ThreadResolution `json:"thread_resolutions,omitempty"`
}

// PullRequestMetadata carries author-supplied context about a pull
// request that is not part of the diff itself: the PR description (the
// author's statement of WHAT the change does and WHY) and the number of
// commits behind it (a signal of how the change was assembled). The
// prompt builder renders it — together with the title and branch names
// already present in the PR header — as an "intent" section so the LLM
// can judge whether the diff actually does what the author claims and
// flag undocumented scope creep. The zero value means "not available":
// the prompt section collapses to nothing and the review proceeds
// exactly as it did before this metadata existed.
type PullRequestMetadata struct {
	// Description is the PR body/description as written by the author.
	// Untrusted content — the prompt builder fences and escape-proofs it
	// exactly like comment bodies and project guidelines. Bounded at load
	// time so a pathological description cannot crowd out the diff.
	Description string
	// CommitCount is the number of commits on the source branch of the
	// PR. 0 means "unknown" (fetch failed or unsupported provider), and
	// the prompt omits the commit line rather than claiming an empty PR.
	CommitCount int
}

// ReviewBatch describes this request's position inside a MULTI-BATCH
// review of a single pull request. A pull request whose assembled prompt
// exceeds the model's context window is not abandoned: the command layer
// splits its files into batches and reviews them one after another (see
// `commands.batchReviewer`), then merges the per-batch results into one
// review. Each of those calls carries a populated ReviewBatch so the
// prompt can tell the model it is looking at a SLICE of the change.
//
// Without that framing the model reviews a partial diff as if it were the
// whole PR and reports false findings — "this function is never called",
// "the new flag has no tests", "the migration has no rollback" — when the
// caller, the test, and the rollback are simply in another batch.
//
// The zero value means "this request covers the whole pull request", and
// every prompt section keyed on it collapses to nothing, so a normal
// single-pass review keeps its historical byte-for-byte prompt shape.
type ReviewBatch struct {
	// Index is the 1-based position of this batch in the run. 0 on a
	// single-pass review.
	Index int
	// Files is the number of changed files carried by THIS batch.
	Files int
	// TotalFiles is the number of changed files in the whole pull
	// request, so the prompt can state the slice's proportion.
	TotalFiles int
}

// IsPartial reports whether the request carries only a slice of the pull
// request's changed files — the condition every batch-aware prompt
// section is gated on. A zero value, or a batch that happens to carry
// every changed file, is NOT partial: there is nothing the model must be
// warned about, so the prompt stays in its single-pass shape.
func (b ReviewBatch) IsPartial() bool {
	return b.Index > 0 && b.Files > 0 && b.Files < b.TotalFiles
}

// ReviewRequest encapsulates the input needed to perform a review.
type ReviewRequest struct {
	Repository  forgeEntities.Repository
	PullRequest forgeEntities.PullRequestDetail
	Diffs       []FileDiff
	Rules       []Rule

	// Batch marks this request as one slice of a pull request that was
	// too large to review in a single pass. Zero on the normal path.
	// See [ReviewBatch] for why the model has to be told.
	Batch ReviewBatch

	// Metadata carries the PR's author-supplied context (description,
	// commit count) fetched from the provider at review time. Zero when
	// the operator disabled the feature (`ai.pr_metadata: false`), the
	// provider has no metadata fetcher, or the fetch failed — the review
	// proceeds without it and the prompt keeps its metadata-free shape.
	Metadata PullRequestMetadata

	// Conversation carries the bot's prior inline review threads on this
	// PR plus every reply on each one, so a re-review session triggered
	// by an `@code-guru` mention can address user pushback or follow-up
	// questions inline rather than re-emitting the same comment shape
	// the user just discussed. Empty for first-pass reviews; populated
	// only when `ReviewOptions.UserMentioned` is true. See
	// `support.BuildReviewConversation` for the assembly contract.
	Conversation []ReviewThread

	// Attempt is the 1-based retry attempt number for this review. The
	// retry decorator (`infrastructure/repositories.RetryingAIReviewer`)
	// sets it before each call so the prompt builder can reinforce the
	// "respond with ONLY valid JSON" instruction on attempts after the
	// first — the common reason a re-sample succeeds where the first try
	// returned prose. 0 or 1 means the first attempt (no reinforcement),
	// keeping that prompt byte-for-byte identical to its pre-retry shape.
	Attempt int

	// ProjectGuidelines carries the reviewed repository's own AI
	// guidance file (its root `CLAUDE.md`), fetched from the provider at
	// review time so the LLM judges the diff against the project's OWN
	// conventions rather than only the operator-configured rules. Empty
	// when the repository has no such file, when the fetch failed (the
	// review proceeds without it), when the operator disabled the
	// feature via `ai.project_guidelines: false`, or when the PR itself
	// modifies the file (the diff already shows it, so the pre-change
	// copy is not fetched on top). Content is truncated at load time so
	// a pathological guidelines file cannot blow the prompt budget.
	ProjectGuidelines string
}

// ReviewThread is one inline review conversation: the bot's original
// comment plus every reply (from the user, the bot itself, or other
// reviewers) in chronological order. Used as input to the LLM prompt
// during a re-review so the model can read the dialogue before
// deciding whether to repeat, withdraw, or respond to the original
// finding.
type ReviewThread struct {
	FilePath string
	Line     int
	// ThreadID is the provider-specific thread identifier (Azure
	// DevOps thread ID; GitHub review-comment ID stand-in). Used by
	// the post-pipeline to call `UpdatePullRequestThreadStatus` when
	// the LLM marks the thread as resolved, so the operator does not
	// have to dismiss a "fixed" thread by hand. Zero on platforms
	// that do not return a usable thread ID.
	ThreadID int64
	// RootCommentID is the provider's ID for the top-level bot
	// comment that started the thread. Surfaced so the post-pipeline
	// can dedup follow-up replies against the same root and so future
	// "edit-on-second-push" features have a stable handle on the
	// original comment.
	RootCommentID int64
	// Comments is the chronological message list — index 0 is the
	// thread root (always one of the bot's prior comments because
	// the conversation walk starts from the bot's own thread roots).
	Comments []ReviewMessage
}

// ThreadResolution is the AI's verdict on a single prior bot review
// thread on the mention re-review path. The post-pipeline turns it into
// a thread-status update + a short reply so the PR author sees the bot
// engaging with each existing thread instead of opening a parallel
// flood of new comments that say the same thing.
//
// Status values map to provider thread states:
//
//   - `resolved`   — the diff or the user's reply addressed the
//     original concern; the bot posts a brief confirmation and updates
//     the thread status to `fixed` so it stops blocking review.
//   - `outstanding` — the original concern is still valid; the bot
//     posts a short restated reply and leaves the thread `active`.
//   - `outdated`   — the original concern no longer applies (e.g. the
//     code in question was deleted, the conversation moved on); the
//     bot soft-closes the thread without making a content claim.
//
// `ID` is the synthetic per-prompt identifier (e.g. `T1`, `T2`) the
// user prompt renders next to each prior thread (`### Thread T1 on
// <file>:<line>`). It is the durable, unambiguous key for matching a
// resolution back to its thread when multiple historical bot threads
// share the same `<file>:<line>` anchor — without it the post-pipeline
// would collapse them onto one entry and silently lose every
// resolution past the first. `FilePath` + `Line` remain on the entry
// as a human-readable hint and as a fallback for LLM responses that
// drop the id (defensive); the post-pipeline only falls back when the
// anchor is unique within the prompt.
type ThreadResolution struct {
	ID          string `json:"id,omitempty"`
	FilePath    string `json:"file"`
	Line        int    `json:"line"`
	Status      string `json:"status"`
	Explanation string `json:"explanation,omitempty"`
}

// ReviewMessage is one entry in a ReviewThread's reply chain. Author
// is the platform-specific identity (login on GitHub; uniqueName /
// displayName on Azure DevOps); the LLM reads it as a free-form
// label, so providers populate whatever shape is most readable.
type ReviewMessage struct {
	Author string
	Body   string
}

// Rule represents a single review rule loaded from a markdown file.
type Rule struct {
	Name      string
	Category  string
	Content   string
	FileGlobs []string
}
