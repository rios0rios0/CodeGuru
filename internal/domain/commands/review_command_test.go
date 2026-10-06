package commands_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	logger "github.com/sirupsen/logrus"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/commands"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/domain/repositories"
	"github.com/rios0rios0/codeguru/internal/support"
	doubles "github.com/rios0rios0/codeguru/test/domain/doubles/repositories"
)

// `shouldPostSummary` and the standalone summary-thread post in
// `postComments` were removed once the completion annotation
// (`postReviewCompleteAnnotation`, since PR #124) gained a paragraph
// for `result.Summary`. Posting the summary in both places left a
// duplicate PR-wide thread on every clean review. The
// `TestExecuteLLMPathSubmitsNativeReviewWithEmptyBody` row below
// pins the new contract: on a clean LLM review the only PR-wide
// comments are the "reviewing" marker and the completion annotation,
// and the summary appears exactly once (inside the annotation).

// TestFilterStaleComments pins the staleness-filter contract that
// keeps the bot from posting comments on files no longer present in
// the latest PR iteration. Captured live on
// `internal-app/internal-integrator#NNNN` where every bot comment
// rendered with ADO's "this file no longer exists in the latest pull
// request changes" warning because the PR had been rewritten between
// the webhook firing (when the diff was fetched) and the review
// completing (when the comments were posted).
func TestFilterStaleComments(t *testing.T) {
	t.Parallel()

	t.Run("should keep all comments when every path is still in the live set", func(t *testing.T) {
		t.Parallel()

		// given
		comments := []entities.ReviewComment{
			{FilePath: "main.go", Line: 10, Body: "fix this"},
			{FilePath: "internal/foo.go", Line: 1, Body: "and this"},
		}
		live := map[string]struct{}{"main.go": {}, "internal/foo.go": {}}

		// when
		kept, dropped := commands.FilterStaleComments(comments, live)

		// then
		assert.Equal(t, comments, kept, "every comment must survive when its file is still in the diff")
		assert.Empty(t, dropped, "no comment should be flagged as stale")
	})

	t.Run("should drop only the comments whose path is no longer in the live set", func(t *testing.T) {
		t.Parallel()

		// given: AI returned 3 findings; only `main.go` survived the
		// follow-up push. The other two reference files the latest
		// iteration no longer touches and would render with the
		// ADO "file no longer exists" banner.
		comments := []entities.ReviewComment{
			{FilePath: "main.go", Line: 10, Body: "still relevant"},
			{FilePath: "removed_in_squash.go", Line: 5, Body: "ignored"},
			{FilePath: "renamed_old.go", Line: 1, Body: "ignored too"},
		}
		live := map[string]struct{}{"main.go": {}}

		// when
		kept, dropped := commands.FilterStaleComments(comments, live)

		// then
		require.Len(t, kept, 1)
		assert.Equal(t, "main.go", kept[0].FilePath)
		require.Len(t, dropped, 2)
		assert.Equal(t, "removed_in_squash.go", dropped[0].FilePath)
		assert.Equal(t, "renamed_old.go", dropped[1].FilePath)
	})

	t.Run("should drop every comment when the live set is empty", func(t *testing.T) {
		t.Parallel()

		// given: a defensive case — empty live set drops everything.
		// In production this happens when `GetPullRequestFiles`
		// returns an empty list (e.g. a force-pushed PR where every
		// file was reverted). The bot must not post a single inline
		// comment in that case.
		comments := []entities.ReviewComment{
			{FilePath: "a.go", Line: 1, Body: "..."},
			{FilePath: "b.go", Line: 2, Body: "..."},
		}

		// when
		kept, dropped := commands.FilterStaleComments(comments, map[string]struct{}{})

		// then
		assert.Empty(t, kept)
		assert.Len(t, dropped, 2)
	})

	t.Run("should normalise leading slash so AI paths match ADO-style paths", func(t *testing.T) {
		t.Parallel()

		// given: ADO's `GetPullRequestFiles` returns paths like
		// `/internal/foo.go` while the AI (driven from the diff) emits
		// `internal/foo.go`. Both halves of the pipeline must compare
		// after stripping the leading `/`, mirroring the rule
		// `support.LookupChunkByPath` already applies on the diff
		// side. Without this row a future "let me clean up the path
		// normalisation" refactor would silently drop every comment
		// on every ADO PR.
		comments := []entities.ReviewComment{
			{FilePath: "internal/foo.go", Line: 1, Body: "..."},
		}
		live := map[string]struct{}{"internal/foo.go": {}} // value already normalised by the caller

		// when
		kept, dropped := commands.FilterStaleComments(comments, live)

		// then
		assert.Len(t, kept, 1, "AI path with no leading slash must match the normalised live entry")
		assert.Empty(t, dropped)
	})

	t.Run("should normalise an AI-supplied leading slash too (defence in depth)", func(t *testing.T) {
		t.Parallel()

		// given: belt-and-suspenders — if the AI ever emits a path
		// with a leading slash (e.g. because the prompt or a future
		// model behaviour change includes one), the filter must not
		// flag it as stale just because the live set's keys are
		// stored without the prefix.
		comments := []entities.ReviewComment{
			{FilePath: "/internal/foo.go", Line: 1, Body: "..."},
		}
		live := map[string]struct{}{"internal/foo.go": {}}

		// when
		kept, dropped := commands.FilterStaleComments(comments, live)

		// then
		assert.Len(t, kept, 1)
		assert.Empty(t, dropped)
	})

	t.Run("should return nil/nil when the input list is empty", func(t *testing.T) {
		t.Parallel()

		// given
		live := map[string]struct{}{"main.go": {}}

		// when
		kept, dropped := commands.FilterStaleComments(nil, live)

		// then
		assert.Empty(t, kept)
		assert.Empty(t, dropped)
	})

	t.Run("should keep PR-wide comments even when their FilePath looks stale", func(t *testing.T) {
		t.Parallel()

		// given: PR-wide comments (`Line <= 0`) are posted via
		// `PostPullRequestComment`, which renders them as repository-
		// wide annotations with no file:line anchor — so they cannot
		// produce ADO's "file no longer exists" warning. Dropping
		// them on staleness grounds would silently delete legitimate
		// summary feedback. Pinned per Copilot review on PR #99
		// thread `PRRT_kwDOJKAEo85-5obu`.
		comments := []entities.ReviewComment{
			{FilePath: "removed.go", Line: 0, Body: "summary annotation, no anchor"},
			{FilePath: "", Line: 0, Body: "no path at all"},
			{FilePath: "removed.go", Line: 5, Body: "inline — must be dropped"},
		}
		live := map[string]struct{}{"main.go": {}}

		// when
		kept, dropped := commands.FilterStaleComments(comments, live)

		// then
		require.Len(t, kept, 2, "PR-wide annotations stay regardless of FilePath")
		assert.Equal(t, "summary annotation, no anchor", kept[0].Body)
		assert.Equal(t, "no path at all", kept[1].Body)
		require.Len(t, dropped, 1, "only the inline reference to a removed file is dropped")
		assert.Equal(t, "inline — must be dropped", dropped[0].Body)
	})
}

func TestSummarizeStaleFilePaths(t *testing.T) {
	t.Parallel()

	t.Run("should join unique paths with a comma when the count is small", func(t *testing.T) {
		t.Parallel()

		// given
		dropped := []entities.ReviewComment{
			{FilePath: "a.go"}, {FilePath: "b.go"}, {FilePath: "a.go"},
		}

		// when
		got := commands.SummarizeStaleFilePaths(dropped)

		// then
		assert.Equal(t, "a.go, b.go", got, "duplicates must be deduplicated to keep the log line tight")
	})

	t.Run("should append `(+N more)` when the unique count exceeds the cap", func(t *testing.T) {
		t.Parallel()

		// given: nine unique paths, cap is eight
		var dropped []entities.ReviewComment
		for _, p := range []string{"a.go", "b.go", "c.go", "d.go", "e.go", "f.go", "g.go", "h.go", "i.go"} {
			dropped = append(dropped, entities.ReviewComment{FilePath: p})
		}

		// when
		got := commands.SummarizeStaleFilePaths(dropped)

		// then
		assert.Contains(t, got, "(+1 more)",
			"the trailing sentinel must reflect how many paths were elided so the operator knows the count")
		assert.Less(t, strings.Count(got, ".go"), 9,
			"the visible portion must not exceed the cap")
	})

	t.Run("should return an empty string for an empty input", func(t *testing.T) {
		t.Parallel()

		// given: a nil/empty `dropped` slice — every other subtest in
		// this file (and across the repo) keeps the BDD `given/when/
		// then` triplet even when the setup is trivial. CLAUDE.md
		// requires all three markers, so leaving `// given` empty is
		// preferable to omitting it entirely.

		// when
		got := commands.SummarizeStaleFilePaths(nil)

		// then
		assert.Empty(t, got)
	})

	t.Run("should deduplicate by normalised path so leading-slash variants are not double-counted", func(t *testing.T) {
		t.Parallel()

		// given: the AI sometimes emits `internal/foo.go` and ADO's
		// underlying paths look like `/internal/foo.go` — both
		// references resolve to the same file, so the operator log
		// must list each file once. Pinned per Copilot review on
		// PR #99 thread `PRRT_kwDOJKAEo85-5obx`.
		dropped := []entities.ReviewComment{
			{FilePath: "internal/foo.go"},
			{FilePath: "/internal/foo.go"},
			{FilePath: "main.go"},
		}

		// when
		got := commands.SummarizeStaleFilePaths(dropped)

		// then
		assert.Equal(t, "internal/foo.go, main.go", got,
			"the normalised pair counts once and the first form encountered is the one printed")
	})
}

func TestNormalizeFilePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "should strip a single leading slash", in: "/internal/foo.go", want: "internal/foo.go"},
		{name: "should leave an unprefixed path alone", in: "internal/foo.go", want: "internal/foo.go"},
		{name: "should not strip a non-leading slash", in: "internal/foo.go/", want: "internal/foo.go/"},
		{name: "should preserve an empty input", in: "", want: ""},
		{
			name: "should only strip ONE leading slash (so a network-style `//host/path` does not collapse)",
			// The bot will never see this in real ADO traffic, but
			// pinning the contract prevents a future "be more
			// aggressive" refactor from silently turning `//`
			// into `/`.
			in:   "//double-leading.go",
			want: "/double-leading.go",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// when
			got := commands.NormalizeFilePathForTest(tc.in)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBuildReviewingMarkerBody(t *testing.T) {
	t.Parallel()

	t.Run("should render the marker with the start timestamp in RFC 3339 UTC", func(t *testing.T) {
		t.Parallel()

		// given: a fixed timestamp pinned in UTC so the formatting
		// contract is deterministic. RFC 3339 matches the shape on
		// the corresponding `Info` log line emitted by
		// `postReviewingMarker` (`started_at=<ts>`), so a reader can
		// correlate the timestamp shown in the PR thread with the
		// log entry produced for the same review.
		ts := time.Date(2026, 5, 1, 1, 52, 21, 0, time.UTC)

		// when
		body := commands.BuildReviewingMarkerBody(ts)

		// then
		assert.Contains(t, body, "Code Guru is reviewing this PR.",
			"the marker headline must explicitly tell the author the bot has the PR")
		assert.Contains(t, body, "Started at 2026-05-01T01:52:21Z.",
			"the timestamp must be RFC 3339 UTC so it matches the operator log shape")
		assert.Contains(t, body, "Comments will be posted as inline threads",
			"the body must set expectations on what the eventual review looks like")
	})

	t.Run("should always emit a non-empty body even at the zero time (defensive)", func(t *testing.T) {
		t.Parallel()

		// given: the zero `time.Time` shouldn't crash or produce an
		// empty body — pin the contract so a future refactor that
		// (e.g.) reads the year off the timestamp doesn't panic on
		// an uninitialised value.

		// when
		body := commands.BuildReviewingMarkerBody(time.Time{})

		// then
		assert.NotEmpty(t, body)
		assert.Contains(t, body, "Code Guru is reviewing this PR.")
	})

	t.Run("should normalise a non-UTC input to UTC so the printed timestamp ends in Z", func(t *testing.T) {
		t.Parallel()

		// given: a caller passing in a localised `time.Time` (e.g. an
		// `America/Sao_Paulo` clock that mistakenly skipped the
		// `.UTC()` step). The helper must enforce its own contract
		// rather than echoing the caller's location into the rendered
		// body — pinned per Copilot review on PR #102 thread
		// `PRRT_kwDOJKAEo85-56Sq`. Without the defensive `.UTC()`
		// inside `buildReviewingMarkerBody`, the body would render
		// `Started at 2026-04-30T22:52:21-03:00.` and the documented
		// "RFC 3339 in UTC" contract would silently break. Use
		// `time.FixedZone` rather than `time.LoadLocation`: the
		// latter reads `tzdata` at runtime and silently fails with
		// `nil` on hermetic systems (Alpine, distroless, scratch),
		// which would then panic in `time.Date`.
		spLoc := time.FixedZone("America/Sao_Paulo", -3*60*60)
		ts := time.Date(2026, 4, 30, 22, 52, 21, 0, spLoc) // == 2026-05-01T01:52:21Z

		// when
		body := commands.BuildReviewingMarkerBody(ts)

		// then
		assert.Contains(t, body, "Started at 2026-05-01T01:52:21Z.",
			"the helper must format in UTC regardless of the input Location")
		assert.NotContains(t, body, "-03:00", "no timezone offset should leak into the body")
	})

	t.Run("should not embed `\\n` literally (must use real newlines for Markdown rendering)", func(t *testing.T) {
		t.Parallel()

		// given: ADO and GitHub render the marker as Markdown — the
		// blank line between the headline and the explanatory
		// paragraph requires an actual `\n\n` so the renderer treats
		// them as separate paragraphs. A literal "\n" string would
		// render as the four characters `\`, `n`, `\`, `n` and the
		// PR thread would look like a single squashed line.
		ts := time.Date(2026, 5, 1, 1, 0, 0, 0, time.UTC)

		// when
		body := commands.BuildReviewingMarkerBody(ts)

		// then
		assert.NotContains(t, body, `\n`, "the body must contain real newlines, not the escape sequence")
		assert.Contains(t, body, "\n\n", "the body must have at least one blank line for Markdown paragraph breaks")
	})
}

func TestBuildReviewFailedBody(t *testing.T) {
	t.Parallel()

	// The failure annotation is posted ONLY after every retry attempt has
	// failed (the RetryingAIReviewer decorator). Its defining contract is
	// that it carries a short, classified reason and NEVER the raw error —
	// a transient backend failure embeds the model's raw output (e.g. the
	// claude CLI's "API Error: socket connection closed" JSON envelope),
	// and posting that to the PR is the leak this body must not produce.

	t.Run("should render the headline, the next-step hint and the UTC timestamp", func(t *testing.T) {
		t.Parallel()

		// given
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		err := errors.New("boom")

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "Code Guru review failed.",
			"the headline must be unambiguous so the author does not confuse it with the marker")
		assert.Contains(t, body, "Failed at 2026-05-01T02:51:21Z.",
			"the timestamp must match the operator-log RFC 3339 UTC shape")
		assert.Contains(t, body, "@code-guru",
			"the body must tell the author how to retry the review")
	})

	t.Run("should NEVER echo the raw error text into the PR body", func(t *testing.T) {
		t.Parallel()

		// given: a generic backend error carrying a distinctive raw token
		// that stands in for the claude CLI's leaked socket-error envelope.
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		err := errors.New("claude CLI failed: API Error: socket connection closed RAW_ENVELOPE_TOKEN")

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.NotContains(t, body, "RAW_ENVELOPE_TOKEN",
			"the raw model/backend output must never reach the PR — that is the leak this rewrite removes")
		assert.NotContains(t, body, "socket connection closed")
		assert.Contains(t, body, "The AI backend errored",
			"a non-parse error must surface the generic 'backend errored' category")
	})

	t.Run("should classify an unparseable-response error as a JSON-format failure", func(t *testing.T) {
		t.Parallel()

		// given: the sentinel wrapped the way the retry decorator wraps it
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		err := fmt.Errorf("AI review failed after 3 attempt(s): %w", support.ErrUnparseableResponse)

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "expected JSON format",
			"an ErrUnparseableResponse (even wrapped by the retry envelope) must classify as a JSON-format failure")
		assert.NotContains(t, body, "not valid JSON, even after repair",
			"the raw sentinel text must not be echoed — only the friendly classification")
	})

	t.Run("should render operator-facing guidance for an OS argument-limit refusal", func(t *testing.T) {
		t.Parallel()

		// given: the sentinel wrapped the way the backend wraps it. Nothing
		// about the diff caused this failure — the oversized argument is the
		// SYSTEM prompt, assembled from the operator's rule corpus — so the
		// body must not blame the pull request or ask the author to act.
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		err := fmt.Errorf("claude CLI failed: %w", support.ErrArgumentListTooLong)

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "**Code Guru review",
			"the headline must carry the completion marker so the review-once gate stops the re-fail loop")
		assert.Contains(t, body, "rules.categories",
			"the body must name the knob the operator actually turns to fix it")
		assert.Contains(t, body, "not a problem with this pull request",
			"the author must be told the diff was never read, so they do not go looking for a defect")
		assert.NotContains(t, body, "push a new commit",
			"the generic retry advice is actively wrong here — the next exec is refused identically")
		assert.NotContains(t, body, "Split it into several smaller",
			"this is not a too-large PR; splitting the diff changes nothing")
		assert.Contains(t, body, "Failed at 2026-05-01T02:51:21Z.")
	})

	t.Run("should normalise a non-UTC input to UTC so the printed timestamp ends in Z", func(t *testing.T) {
		t.Parallel()

		// given: defensive — same contract as `buildReviewingMarkerBody`.
		// Use `time.FixedZone` rather than `time.LoadLocation` (which reads
		// `tzdata` at runtime and returns nil on hermetic images).
		spLoc := time.FixedZone("America/Sao_Paulo", -3*60*60)
		ts := time.Date(2026, 4, 30, 23, 51, 21, 0, spLoc) // == 2026-05-01T02:51:21Z

		// when
		body := commands.BuildReviewFailedBody(ts, errors.New("transient"), commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "Failed at 2026-05-01T02:51:21Z.",
			"the helper must format in UTC regardless of the input Location")
		assert.NotContains(t, body, "-03:00", "no timezone offset should leak into the body")
	})

	t.Run("should produce a readable body when the error is nil (defensive)", func(t *testing.T) {
		t.Parallel()

		// given: callers always pass a non-nil error, but belt-and-
		// suspenders — a nil must not render the literal `<nil>`.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

		// when
		body := commands.BuildReviewFailedBody(ts, nil, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "could not be completed")
		assert.NotContains(t, body, "<nil>")
	})

	t.Run("should stay bounded even when the error is huge (no raw echo)", func(t *testing.T) {
		t.Parallel()

		// given: a runaway backend that emits a 10 KB error. Because the
		// body never echoes the raw error, the rendered body stays small
		// regardless of the error size — the strongest form of the bound.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		oversized := errors.New(strings.Repeat("X", 10*1024))

		// when
		body := commands.BuildReviewFailedBody(ts, oversized, commands.ReviewFailureContext{})

		// then
		assert.Less(
			t,
			len(body),
			1024,
			"the body must not grow with the error size — the raw error is never echoed, so a runaway backend cannot flood the PR",
		)
		assert.NotContains(t, body, strings.Repeat("X", 100),
			"none of the raw error content may appear in the body")
	})

	// A context-window overflow is the failure this whole change targets: the
	// PR is too large, so the annotation must explain THAT (not "usually
	// transient") and tell the author to shrink the change — never to push
	// another commit, which only grows the diff.

	t.Run("should render dedicated 'too large' guidance for a context-window failure", func(t *testing.T) {
		t.Parallel()

		// given: the sentinel wrapped the way a backend + retry decorator wrap it
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		err := fmt.Errorf("%w (anthropic: prompt is too long)", support.ErrContextWindowExceeded)
		sizeCtx := commands.ReviewFailureContext{FileCount: 180, DiffBytes: 1887436}

		// when
		body := commands.BuildReviewFailedBody(ts, err, sizeCtx)

		// then
		assert.Contains(t, body, "too large for the AI model's context window",
			"the author must learn the real cause, not a generic 'backend errored'")
		assert.Contains(t, body, "180 files",
			"the annotation must quantify the scale so the author sees how much is too much")
		assert.Contains(t, body, "1.8 MB",
			"the diff size must be humanised so the scale is legible at a glance")
		assert.Contains(t, body, "Split it into several smaller",
			"the fix guidance must point at splitting the PR, the reliable remedy")
		assert.Contains(
			t,
			body,
			"**Code Guru review",
			"the body must carry the review-once marker so a too-large PR is not re-reviewed (and re-failed) on every push",
		)
		assert.Contains(t, body, "Failed at 2026-05-01T02:51:21Z.")
	})

	t.Run("should NOT tell the author to retry or mention the bot on a too-large failure", func(t *testing.T) {
		t.Parallel()

		// given: retrying a too-large PR re-runs the identical failure, and
		// "push a new commit" makes the diff bigger — both are wrong here.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		err := fmt.Errorf("%w (openai: maximum context length is 128000 tokens)", support.ErrContextWindowExceeded)

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.NotContains(t, body, "@code-guru",
			"a too-large PR must not be told to mention the bot — that re-runs the same failure")
		assert.NotContains(t, body, "push a new commit",
			"a too-large PR must not be told to push more commits — that only grows the diff")
		assert.NotContains(t, body, "prompt is too long",
			"the raw backend message must never reach the PR body")
		assert.Contains(t, body, "too large for the AI model's context window")
	})

	t.Run("should omit the scale figures when the PR size is unknown", func(t *testing.T) {
		t.Parallel()

		// given: a zero-value context (scale not measured) still renders a
		// coherent, actionable body — just without the concrete numbers.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		err := fmt.Errorf("%w (claude: prompt is too long)", support.ErrContextWindowExceeded)

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "larger than the AI reviewer can read in a single pass")
		assert.Contains(t, body, "Split it into several smaller")
		assert.NotContains(t, body, "changes **",
			"with no measured scale the body must not emit an empty '**0 files**' clause")
	})

	// A content-safety refusal is the failure this change targets: the AI's
	// safety system declined to review the content (common for security code).
	// The notice must explain THAT, reassure the author it is not a defect, and
	// point at the real remedies — never suggest retrying.

	t.Run("should render dedicated content-safety guidance naming the category", func(t *testing.T) {
		t.Parallel()

		// given: the typed refusal wrapped the way a backend wraps it
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		err := fmt.Errorf("anthropic: %w", &support.ContentSafetyRefusalError{Category: "cyber"})

		// when
		body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "content-safety system declined",
			"the author must learn the real cause — the AI declined the content")
		assert.Contains(t, body, "cybersecurity-related content",
			"the `cyber` category must surface as a human-readable phrase")
		assert.Contains(t, body, "not** a judgment that the change",
			"the notice must reassure the author the PR is not being flagged as malicious")
		assert.Contains(t, body, "Request a review from a human",
			"the guidance must point at a manual review, the reliable remedy")
		assert.Contains(t, body, "refusal_fallback_model",
			"operators must learn the fallback-model lever")
		assert.Contains(t, body, "**Code Guru review",
			"the body must carry the review-once marker so a refused PR is not re-reviewed every push")
		assert.Contains(t, body, "Failed at 2026-05-01T02:51:21Z.")
	})

	t.Run(
		"should fall back to generic phrasing and never suggest retrying on an uncategorised refusal",
		func(t *testing.T) {
			t.Parallel()

			// given: a refusal with no category (e.g. OpenAI content_filter)
			ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			err := error(&support.ContentSafetyRefusalError{})

			// when
			body := commands.BuildReviewFailedBody(ts, err, commands.ReviewFailureContext{})

			// then
			assert.Contains(t, body, "flagged this change",
				"an unknown/empty category must fall back to the generic phrase, not render an empty clause")
			assert.NotContains(t, body, "@code-guru",
				"a refused PR must not be told to mention the bot — that re-runs the same refusal")
			assert.NotContains(t, body, "push a new commit",
				"a refused PR must not be told to push more commits — the same content is re-evaluated")
		},
	)
}

// TestReviewFailureContextLeadSentence pins the scale sentence that opens
// both too-large notices (the "review failed" annotation and the "reviewing
// in batches" notice), so the two cannot drift apart in how they quantify
// the change.
func TestReviewFailureContextLeadSentence(t *testing.T) {
	t.Parallel()

	const unknownScale = "It is larger than the AI reviewer can read in a single pass"

	t.Run("should quantify the file count and the diff size when both are known", func(t *testing.T) {
		t.Parallel()

		// given
		sizeCtx := commands.ReviewFailureContext{FileCount: 180, DiffBytes: 1887436}

		// when
		lead := commands.LeadSentence(sizeCtx, unknownScale)

		// then
		assert.Equal(t,
			"It changes **180 files** (~1.8 MB of diff), "+
				"which is more than the AI reviewer can read in a single pass",
			lead)
	})

	t.Run("should omit the diff size when only the file count is known", func(t *testing.T) {
		t.Parallel()

		// given
		sizeCtx := commands.ReviewFailureContext{FileCount: 1}

		// when
		lead := commands.LeadSentence(sizeCtx, unknownScale)

		// then
		assert.Equal(t,
			"It changes **1 file**, which is more than the AI reviewer can read in a single pass",
			lead)
	})

	t.Run("should return the caller's wording verbatim when the scale is unknown", func(t *testing.T) {
		t.Parallel()

		// given
		batched := "This pull request is larger than the AI reviewer can read in a single pass"

		// when
		lead := commands.LeadSentence(commands.ReviewFailureContext{}, batched)

		// then
		assert.Equal(t, batched, lead)
		assert.NotContains(t, lead, "**", "no scale figures may be emitted for an unmeasured change")
	})
}

func TestReviewFailedBodyAlwaysSetsReviewOnceMarker(t *testing.T) {
	t.Parallel()

	// Every "review failed" body MUST contain the review-once marker
	// (`support.HasCompletedReviewMarker` scans for `**Code Guru review`) so a
	// failed review — of ANY class — gates the next push and does not flood
	// the PR with duplicate annotations. A body that reworded the headline out
	// of this substring (as the first context-window draft did) is exactly the
	// regression this test exists to catch.
	ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]error{
		"generic":        errors.New("boom"),
		"unparseable":    support.ErrUnparseableResponse,
		"context window": fmt.Errorf("%w (anthropic: prompt is too long)", support.ErrContextWindowExceeded),
		"content safety": fmt.Errorf("anthropic: %w", &support.ContentSafetyRefusalError{Category: "cyber"}),
	}
	for name, reviewErr := range cases {
		t.Run("should set the review-once marker for the "+name+" failure", func(t *testing.T) {
			t.Parallel()

			// given / when: exercise the real gate function on the rendered body
			body := commands.BuildReviewFailedBody(
				ts, reviewErr, commands.ReviewFailureContext{FileCount: 5, DiffBytes: 1024},
			)

			// then
			require.True(t, support.HasCompletedReviewMarker([]string{body}),
				"the failure body must carry the review-once marker so the PR is not re-reviewed on every push")
		})
	}
}

func TestReviewFailureContextFrom(t *testing.T) {
	t.Parallel()

	t.Run("should count files and sum the assembled diff bytes", func(t *testing.T) {
		t.Parallel()

		// given
		files := []forgeEntities.PullRequestFile{{Path: "a.go"}, {Path: "b.ts"}, {Path: "c.md"}}
		diffs := []entities.FileDiff{
			{Path: "a.go", Diff: "12345"},   // 5 bytes
			{Path: "b.ts", Diff: "1234567"}, // 7 bytes
			{Path: "c.md", Diff: ""},        // 0 bytes
		}

		// when
		got := commands.ReviewFailureContextFrom(files, diffs)

		// then
		assert.Equal(t, 3, got.FileCount, "file count comes from the PR file list")
		assert.Equal(t, 12, got.DiffBytes, "diff bytes is the sum of every assembled per-file diff")
	})
}

func TestBuildReviewCompleteBody(t *testing.T) {
	t.Parallel()

	t.Run("should render the completion notice with verdict, comment count and timestamp", func(t *testing.T) {
		t.Parallel()

		// given: a typical AI review result — `request_changes`
		// verdict with 3 inline comments. The body must surface
		// each so the author can see the conclusion at a glance
		// without scrolling.
		ts := time.Date(2026, 5, 1, 2, 51, 21, 0, time.UTC)
		result := &entities.ReviewResult{
			Verdict: "request_changes",
			Comments: []entities.ReviewComment{
				{FilePath: "a.go", Line: 1, Body: "..."},
				{FilePath: "b.go", Line: 2, Body: "..."},
				{FilePath: "c.go", Line: 3, Body: "..."},
			},
		}

		// when
		body := commands.BuildReviewCompleteBody(ts, result)

		// then
		assert.Contains(t, body, "Code Guru review complete.")
		assert.Contains(t, body, "request_changes",
			"the verdict must surface so the author knows the bot's conclusion")
		assert.Contains(t, body, "3 inline comments",
			"the comment count must surface so the author can locate the threads")
		assert.Contains(
			t,
			body,
			"Completed at 2026-05-01T02:51:21Z.",
			"the timestamp must be RFC 3339 UTC matching the marker's `Started at <ts>` shape so a reader can pair them",
		)
	})

	t.Run("should pluralise the comment label correctly for exactly 1 inline comment", func(t *testing.T) {
		t.Parallel()

		// given: pluralisation is the kind of thing that's quiet
		// until a reader notices "1 inline comments" looks broken.
		// Pin "1 inline comment" / "0 inline comments" /
		// "2 inline comments" so a future formatting refactor
		// preserves grammar.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		result := &entities.ReviewResult{
			Verdict:  "comment",
			Comments: []entities.ReviewComment{{FilePath: "x.go", Line: 1, Body: "."}},
		}

		// when
		body := commands.BuildReviewCompleteBody(ts, result)

		// then
		assert.Contains(t, body, "1 inline comment.",
			"singular comment count must use the singular noun")
		assert.NotContains(t, body, "1 inline comments")
	})

	t.Run("should render `0 inline comments` when the AI returned no findings", func(t *testing.T) {
		t.Parallel()

		// given: a clean review — `verdict=approve` and zero
		// inline findings. The completion notice must still post
		// (it's the bot's "done" signal) and must read naturally.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		result := &entities.ReviewResult{Verdict: "approve", Comments: nil}

		// when
		body := commands.BuildReviewCompleteBody(ts, result)

		// then
		assert.Contains(t, body, "approve")
		assert.Contains(t, body, "0 inline comments.",
			"plural form covers zero too — `0 inline comment` would read as broken grammar")
	})

	t.Run("should fall back to `comment` verdict when the result's verdict is empty", func(t *testing.T) {
		t.Parallel()

		// given: the AI parser sometimes yields an empty Verdict
		// (e.g. malformed JSON repaired but missing the field).
		// The completion notice must still render something
		// meaningful rather than `Verdict: \`\`` which looks broken.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
		result := &entities.ReviewResult{Verdict: "", Comments: nil}

		// when
		body := commands.BuildReviewCompleteBody(ts, result)

		// then
		assert.Contains(t, body, "comment",
			"empty verdict must fall back to the `comment` literal")
		assert.NotContains(t, body, "``", "no empty backtick block must leak into the body")
	})

	t.Run("should normalise a non-UTC input to UTC so the printed timestamp ends in Z", func(t *testing.T) {
		t.Parallel()

		// given: defensive — same contract as the other body
		// builders (per Copilot review on PR #102 thread
		// `PRRT_kwDOJKAEo85-56Sq`). Use `time.FixedZone` rather
		// than `time.LoadLocation` for hermeticity (matching the
		// pattern fixed across the rest of this file in PR #103
		// thread `PRRT_kwDOJKAEo85-6Cu4`).
		spLoc := time.FixedZone("America/Sao_Paulo", -3*60*60)
		ts := time.Date(2026, 4, 30, 23, 51, 21, 0, spLoc) // == 2026-05-01T02:51:21Z
		result := &entities.ReviewResult{Verdict: "comment", Comments: nil}

		// when
		body := commands.BuildReviewCompleteBody(ts, result)

		// then
		assert.Contains(t, body, "Completed at 2026-05-01T02:51:21Z.",
			"the helper must format in UTC regardless of the input Location")
		assert.NotContains(t, body, "-03:00", "no timezone offset should leak into the body")
	})

	t.Run("should not panic and produce a usable body when the result is nil (defensive)", func(t *testing.T) {
		t.Parallel()

		// given: the production caller never passes nil, but a
		// future refactor that, e.g., skips the AI call for trivial
		// detection and still wires the completion notice could
		// pass `nil`. The helper must degrade gracefully rather
		// than panic on a nil dereference.
		ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)

		// when
		body := commands.BuildReviewCompleteBody(ts, nil)

		// then
		assert.NotEmpty(t, body)
		assert.Contains(t, body, "Code Guru review complete.")
	})

	t.Run(
		"should count only inline (Line > 0) comments — PR-wide annotations don't inflate the count",
		func(t *testing.T) {
			t.Parallel()

			// given: a review with 2 inline (`Line > 0`) findings and
			// 3 PR-wide annotations (`Line <= 0`). The body says
			// "X inline comments", so only the inline ones count
			// against that label — pinned per Copilot review on PR #104
			// thread `PRRT_kwDOJKAEo85-6ErC`.
			ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			result := &entities.ReviewResult{
				Verdict: "request_changes",
				Comments: []entities.ReviewComment{
					{FilePath: "a.go", Line: 1, Body: "inline 1"},
					{FilePath: "b.go", Line: 5, Body: "inline 2"},
					{FilePath: "c.go", Line: 0, Body: "PR-wide"},
					{FilePath: "", Line: 0, Body: "PR-wide annotation"},
					{FilePath: "d.go", Line: -1, Body: "negative-line PR-wide"},
				},
			}

			// when
			body := commands.BuildReviewCompleteBody(ts, result)

			// then
			assert.Contains(t, body, "2 inline comments.",
				"only the two `Line > 0` comments must show up against the `inline` label")
			assert.NotContains(t, body, "5 inline comments",
				"the three PR-wide annotations must NOT inflate the count")
		},
	)

	t.Run(
		"should include result.Summary as a separate paragraph when non-empty (trivial fast path)",
		func(t *testing.T) {
			t.Parallel()

			// given: a trivial-detector-shaped result. Every detector emits
			// a Summary like the one below; when the trivial path posts its
			// completion annotation, that rationale is the ONLY place the
			// PR author sees why the bot reached the verdict.
			ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			result := &entities.ReviewResult{
				Verdict: "approve",
				Summary: "Documentation-only change detected (2 markdown files). Auto-approved by trivial PR policy.",
			}

			// when
			body := commands.BuildReviewCompleteBody(ts, result)

			// then
			assert.Contains(t, body, "**Code Guru review",
				"the F2 review-once-gate marker must remain in the annotation body")
			assert.Contains(
				t,
				body,
				result.Summary,
				"the trivial detector's Summary must surface in the annotation body — without it the PR author only sees the verdict label and loses the rationale (especially important for `reject` verdicts like the bump-detector's missing-files message)",
			)
		},
	)

	t.Run(
		"should preserve the legacy two-paragraph layout when result.Summary is empty (LLM path)",
		func(t *testing.T) {
			t.Parallel()

			// given: the LLM path typically leaves Summary empty because
			// the rationale lands as inline comments. The completion
			// annotation has shipped with two paragraphs ("review complete"
			// + "Verdict: ... " followed by the timestamp) and we must not
			// regress that layout while wiring the trivial-summary section.
			ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
			result := &entities.ReviewResult{Verdict: "comment"}

			// when
			body := commands.BuildReviewCompleteBody(ts, result)

			// then: between "comments." and "_Completed" there must be
			// exactly one blank line — same as before this PR landed.
			expected := "comments.\n\n_Completed at 2026-05-01T00:00:00Z._"
			assert.Contains(
				t,
				body,
				expected,
				"empty Summary must keep the original layout: a single blank line between the verdict line and the timestamp",
			)
		},
	)
}

// stubPRStatusGetter is a 1-method test double satisfying
// `commands.PullRequestStatusGetter`. Records the call and returns a
// canned status / error.
type stubPRStatusGetter struct {
	status string
	err    error
	calls  int
}

func (s *stubPRStatusGetter) GetPullRequestStatus(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
) (string, error) {
	s.calls++
	return s.status, s.err
}

func TestIsPullRequestClosed(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status string
		err    error
		want   bool
	}{
		{name: "should return true for ADO `completed`", status: "completed", want: true},
		{name: "should return true for ADO `abandoned`", status: "abandoned", want: true},
		{name: "should return true for GitHub `closed`", status: "closed", want: true},
		{name: "should return true for GitHub `merged`", status: "merged", want: true},
		{name: "should normalise mixed case (`Completed`)", status: "Completed", want: true},
		{name: "should normalise upper case (`ABANDONED`)", status: "ABANDONED", want: true},
		{name: "should normalise leading/trailing whitespace (` merged `)", status: " merged ", want: true},
		{name: "should return false for ADO `active`", status: "active", want: false},
		{name: "should return false for GitHub `open`", status: "open", want: false},
		{
			name:   "should return false for an empty status (defensive — webhook payload sometimes ships empty)",
			status: "",
			want:   false,
		},
		{
			name:   "should return false for an unknown future enum value (`merging`) — defer to the worker",
			status: "merging",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// given
			getter := &stubPRStatusGetter{status: tc.status, err: tc.err}
			repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}

			// when
			got := commands.IsPullRequestClosed(context.Background(), getter, repo, 12)

			// then
			assert.Equal(t, tc.want, got)
			assert.Equal(t, 1, getter.calls, "the getter must be called exactly once per check")
		})
	}

	t.Run(
		"should return false (proceed with post) when GetPullRequestStatus errors — best-effort contract",
		func(t *testing.T) {
			// given: a transient ADO outage. The bot must NOT silently
			// drop the review comments because the status check failed —
			// posting on a closed PR is harmless (verified live on PR
			// #NNNN), but skipping a legitimate post would be a
			// regression. Pinned per task #43.
			t.Parallel()
			getter := &stubPRStatusGetter{err: errors.New("ADO 503 Service Unavailable")}
			repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}

			// when
			got := commands.IsPullRequestClosed(context.Background(), getter, repo, 12)

			// then
			assert.False(t, got, "a fetch failure must default to `not closed` so the caller proceeds with posting")
			assert.Equal(t, 1, getter.calls)
		},
	)
}

// recordingReviewProvider satisfies `forgeEntities.ReviewProvider` by
// embedding the interface (zero value is nil) and overriding only
// `PostPullRequestComment`. Any other method call would panic, which
// is fine because the marker post helpers never touch the rest of
// the surface — and a future refactor that DOES introduce another
// call would surface immediately as a panic during this test rather
// than as a silent stub.
type recordingReviewProvider struct {
	forgeEntities.ReviewProvider

	calls                  []recordedPRComment
	submissions            []forgeEntities.ReviewSubmission
	submitErr              error
	getPullRequestFilesErr error
	// files seeds the response of `GetPullRequestFiles`. When nil and
	// no error is set, the method returns an empty slice — same as
	// before this field was added.
	files []forgeEntities.PullRequestFile
	// existingComments seeds the response of `ListPullRequestComments`
	// so tests can simulate an already-reviewed PR (review-once gate)
	// or pre-existing inline comments (dedup pass) without standing up
	// a full provider. Nil means "no comments on the PR yet".
	existingComments []forgeEntities.PullRequestComment
	// listCommentsErr, when set, makes `ListPullRequestComments` return
	// the error instead of `existingComments`. Used by the conversation
	// walk's soft-fail-on-list-error test path.
	listCommentsErr error
	// merges records every call to `MergePullRequest` so tests can
	// assert auto-merge behaviour on the trivial fast path.
	merges   []recordedMerge
	mergeErr error
	// threadComments records every inline `PostPullRequestThreadComment`
	// the command issues so tests can pin the resolution-reply contract
	// (one reply per resolved/outstanding/outdated thread, on the same
	// file:line as the original bot thread).
	threadComments []recordedThreadComment
	// threadCommentErr, when set, is returned by
	// `PostPullRequestThreadComment` so tests can drive the soft-fail
	// path (a reply failure must NOT block the rest of the resolutions
	// or the surrounding review).
	threadCommentErr error
	// threadCommentNextID is the value returned as the new thread ID
	// from `PostPullRequestThreadComment`. Tests rarely care about the
	// returned value (it is captured for the future "edit on second
	// push" feature), so the default of 0 is fine.
	threadCommentNextID int
	// threadStatusUpdates records every
	// `UpdatePullRequestThreadStatus` call so tests can assert the
	// auto-close behaviour for `resolved` / `outdated` resolutions.
	threadStatusUpdates []recordedThreadStatusUpdate
	// threadStatusErr, when set, is returned from
	// `UpdatePullRequestThreadStatus` so the resolution-soft-fail
	// contract can be exercised end-to-end.
	threadStatusErr error
	// replies records every `ReplyToThread` call so tests can pin the
	// in-thread-reply contract (the re-review verdict nests inside the
	// existing thread rather than opening a new same-line comment).
	replies []recordedThreadReply
	// replyErr, when set, is returned by `ReplyToThread` so the
	// resolution-soft-fail path can be exercised on the in-thread route.
	replyErr error
}

// recordedThreadComment captures one inline thread-comment post.
type recordedThreadComment struct {
	filePath string
	line     int
	body     string
}

// recordedThreadReply captures one `ReplyToThread` call — the thread the
// bot replied into and the body it posted.
type recordedThreadReply struct {
	threadID int
	body     string
}

// recordedThreadStatusUpdate captures one
// `UpdatePullRequestThreadStatus` call so tests can pin the auto-close
// contract.
type recordedThreadStatusUpdate struct {
	threadID int
	status   string
}

type recordedMerge struct {
	prID               int
	strategy           string
	bypassPolicy       bool
	bypassReason       string
	deleteSourceBranch bool
}

type recordedPRComment struct {
	body string
	opts []forgeEntities.CommentOption
}

func (r *recordingReviewProvider) PostPullRequestComment(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
	body string,
	opts ...forgeEntities.CommentOption,
) error {
	r.calls = append(r.calls, recordedPRComment{body: body, opts: opts})
	return nil
}

func (r *recordingReviewProvider) SubmitPullRequestReview(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
	sub forgeEntities.ReviewSubmission,
) error {
	r.submissions = append(r.submissions, sub)
	return r.submitErr
}

func (r *recordingReviewProvider) GetPullRequestFiles(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
) ([]forgeEntities.PullRequestFile, error) {
	if r.getPullRequestFilesErr != nil {
		return nil, r.getPullRequestFilesErr
	}
	return r.files, nil
}

func (r *recordingReviewProvider) ListPullRequestComments(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
) ([]forgeEntities.PullRequestComment, error) {
	if r.listCommentsErr != nil {
		return nil, r.listCommentsErr
	}
	return r.existingComments, nil
}

func (r *recordingReviewProvider) MergePullRequest(
	_ context.Context,
	_ forgeEntities.Repository,
	prID int,
	strategy string,
	opts ...forgeEntities.MergeOption,
) error {
	bypass := forgeEntities.ResolveMergeOptions(opts...)
	r.merges = append(r.merges, recordedMerge{
		prID:               prID,
		strategy:           strategy,
		bypassPolicy:       bypass.Enabled,
		bypassReason:       bypass.Reason,
		deleteSourceBranch: bypass.DeleteSourceBranch,
	})
	return r.mergeErr
}

// GetPullRequestStatus reports the PR as `"active"` so the
// closed-mid-flight short-circuit in `Execute` does not skip the
// post-review path. Tests that need a different status can swap to a
// custom recorder.
func (r *recordingReviewProvider) GetPullRequestStatus(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
) (string, error) {
	return "active", nil
}

func (r *recordingReviewProvider) PostPullRequestThreadComment(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
	filePath string,
	line int,
	body string,
	_ ...forgeEntities.CommentOption,
) (int, error) {
	r.threadComments = append(r.threadComments, recordedThreadComment{
		filePath: filePath,
		line:     line,
		body:     body,
	})
	if r.threadCommentErr != nil {
		return 0, r.threadCommentErr
	}
	return r.threadCommentNextID, nil
}

func (r *recordingReviewProvider) ReplyToThread(
	_ context.Context,
	_ forgeEntities.Repository,
	_, threadID int,
	body string,
) (int, error) {
	r.replies = append(r.replies, recordedThreadReply{threadID: threadID, body: body})
	if r.replyErr != nil {
		return 0, r.replyErr
	}
	return r.threadCommentNextID, nil
}

func (r *recordingReviewProvider) UpdatePullRequestThreadStatus(
	_ context.Context,
	_ forgeEntities.Repository,
	_ int,
	threadID int,
	status string,
) error {
	r.threadStatusUpdates = append(r.threadStatusUpdates, recordedThreadStatusUpdate{
		threadID: threadID,
		status:   status,
	})
	return r.threadStatusErr
}

func TestAnnotationThreadStatusContract(t *testing.T) {
	t.Parallel()

	// The constant pins the ADO thread-status string the bot sends.
	// `"closed"` is what ADO renders as "discussion ended" — the
	// right shape for purely informational threads (start marker,
	// review-complete notice, review-failed notice). Without this
	// pin a future "let me try `fixed` instead" refactor would land
	// without a test failure even though it would change how the PR
	// author perceives every annotation we post.
	t.Run("should be 'closed' so ADO renders annotations as ended discussions", func(t *testing.T) {
		t.Parallel()

		// given / when / then
		assert.Equal(t, "closed", commands.AnnotationThreadStatus,
			"the constant must remain 'closed' — see the doc on annotationThreadStatus for why")
	})
}

func TestMarkerHelpersForwardThreadStatusOption(t *testing.T) {
	t.Parallel()

	// Pin the wiring contract: each of the three PR-wide annotation
	// helpers MUST forward `forgeEntities.WithThreadStatus(commands.AnnotationThreadStatus)`
	// on every call to `PostPullRequestComment`. Without this, ADO
	// renders every marker / completion / failure notice as an
	// active thread the PR author has to dismiss by hand — the
	// failure mode operationally observed before gitforge PR #87
	// landed.
	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	prID := 4242

	tests := []struct {
		name   string
		invoke func(rc *commands.ReviewCommand, p *recordingReviewProvider)
	}{
		{
			name: "should forward WithThreadStatus(closed) from postReviewingMarker",
			invoke: func(rc *commands.ReviewCommand, p *recordingReviewProvider) {
				commands.PostReviewingMarker(rc, context.Background(), p, repo, prID)
			},
		},
		{
			name: "should forward WithThreadStatus(closed) from postReviewCompleteAnnotation",
			invoke: func(rc *commands.ReviewCommand, p *recordingReviewProvider) {
				result := &entities.ReviewResult{Verdict: "comment", Summary: "ok"}
				commands.PostReviewCompleteAnnotation(rc, context.Background(), p, repo, prID, result)
			},
		},
		{
			name: "should forward WithThreadStatus(closed) from postReviewFailedAnnotation",
			invoke: func(rc *commands.ReviewCommand, p *recordingReviewProvider) {
				commands.PostReviewFailedAnnotation(
					rc,
					context.Background(),
					p,
					repo,
					prID,
					errors.New("claude crashed"),
					commands.ReviewFailureContext{},
				)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// given: a ReviewCommand built with nil dependencies —
			// the helpers never touch them, and a constructor change
			// that introduces a new dependency would surface as a
			// nil-pointer panic here, not as a silent regression.
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{}

			// when
			tc.invoke(rc, provider)

			// then
			require.Len(t, provider.calls, 1, "each helper must call PostPullRequestComment exactly once")
			require.NotEmpty(t, provider.calls[0].opts, "each helper must forward at least one CommentOption")

			// Resolve the forwarded options through gitforge's helper
			// to confirm the encoded status value is `closed` — this
			// keeps the test focused on the contract that matters even
			// if additional CommentOptions are forwarded in the future.
			resolved := forgeEntities.ResolveCommentOptions(provider.calls[0].opts...)
			assert.Equal(t, commands.AnnotationThreadStatus, resolved,
				"the forwarded options must resolve to the AnnotationThreadStatus constant ('closed')")
		})
	}
}

func TestSubmitNativeReviewFlagGate(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	prID := 4242

	t.Run("should not call provider when SubmitNativeReview is false", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}

		// when
		commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
			"approve", "all good", commands.ReviewOptions{SubmitNativeReview: false})

		// then
		assert.Empty(t, provider.submissions)
	})

	t.Run("should map approve verdict to ReviewVerdictApprove when flag is on", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}

		// when
		commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
			"approve", "looks good", commands.ReviewOptions{SubmitNativeReview: true})

		// then
		require.Len(t, provider.submissions, 1)
		assert.Equal(t, forgeEntities.ReviewVerdictApprove, provider.submissions[0].Verdict)
		assert.Equal(t, "looks good", provider.submissions[0].Body)
	})

	t.Run("should map reject verdict to ReviewVerdictRequestChanges when flag is on", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}

		// when
		commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
			"reject", "blocking", commands.ReviewOptions{SubmitNativeReview: true})

		// then
		require.Len(t, provider.submissions, 1)
		assert.Equal(t, forgeEntities.ReviewVerdictRequestChanges, provider.submissions[0].Verdict)
	})

	t.Run(
		"should map comment verdict to WaitingForAuthor so ADO surfaces vote=-5 and GitHub posts a COMMENT review",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{}

			// when
			commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
				"comment", "FYI", commands.ReviewOptions{SubmitNativeReview: true})

			// then
			require.Len(t, provider.submissions, 1)
			assert.Equal(t, forgeEntities.ReviewVerdictWaitingForAuthor, provider.submissions[0].Verdict)
			assert.Equal(t, "FYI", provider.submissions[0].Body)
		},
	)

	t.Run(
		"should map LLM-vocabulary request_changes verdict to RequestChanges (parser emits this, not 'reject')",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{}

			// when
			commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
				"request_changes", "needs work", commands.ReviewOptions{SubmitNativeReview: true})

			// then: this is the verdict shape that was silently skipped before the
			// mapper learned the LLM vocabulary — see the dev pod logs from
			// 2026-05-01T21:13Z where verdict=request_changes never produced a
			// "native review submission failed" warning AND never produced a vote.
			require.Len(t, provider.submissions, 1)
			assert.Equal(t, forgeEntities.ReviewVerdictRequestChanges, provider.submissions[0].Verdict)
			assert.Equal(t, "needs work", provider.submissions[0].Body)
		},
	)

	t.Run("should swallow provider errors so the worker keeps going", func(t *testing.T) {
		t.Parallel()

		// given: the gitforge error path is documented as best-effort UX,
		// so a transient permission failure must not bubble up to Execute.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{submitErr: errors.New("permission denied")}

		// when / then: no panic, no return — the helper is fire-and-forget
		require.NotPanics(t, func() {
			commands.SubmitNativeReview(rc, context.Background(), provider, repo, prID,
				"approve", "lgtm", commands.ReviewOptions{SubmitNativeReview: true})
		})
		require.Len(t, provider.submissions, 1, "the helper still attempted the call before logging the error")
	})
}

func TestExecuteSkipsDraftsByDefault(t *testing.T) {
	t.Parallel()

	t.Run("should short-circuit with a 'skipped: draft' result when ReviewDrafts is false", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
		pr := forgeEntities.PullRequestDetail{
			ID: 4242, Title: "wip", URL: "https://example/pr/4242",
			IsDraft: true,
		}

		// when
		result, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{})

		// then
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "comment", result.Verdict)
		assert.Contains(t, result.Summary, "draft")
		assert.Empty(t, provider.calls, "no marker / annotation should fire on a skipped draft")
		assert.Empty(t, provider.submissions, "no native review should fire on a skipped draft")
	})

	t.Run("should NOT skip when ReviewDrafts opt-in is set", func(t *testing.T) {
		t.Parallel()

		// given: a deterministic provider that surfaces a known error from
		// GetPullRequestFiles — Execute must reach that call (proving the
		// draft branch was bypassed) and return the wrapped error so the
		// test asserts the bypass without relying on panic behaviour.
		expectedErr := errors.New("get pull request files failed")
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			getPullRequestFilesErr: expectedErr,
		}
		repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
		pr := forgeEntities.PullRequestDetail{
			ID: 4242, Title: "wip", URL: "https://example/pr/4242",
			IsDraft: true,
		}

		// when
		result, err := rc.Execute(
			context.Background(), provider, repo, pr,
			commands.ReviewOptions{ReviewDrafts: true},
		)

		// then
		require.Error(t, err)
		require.ErrorIs(
			t,
			err,
			expectedErr,
			"with ReviewDrafts=true the draft branch must be bypassed; Execute must surface the deterministic provider error from GetPullRequestFiles",
		)
		assert.Nil(t, result)
		assert.Empty(
			t,
			provider.submissions,
			"the command should stop on the deterministic provider error instead of skipping the draft",
		)
	})
}

func TestDropDuplicateComments(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242

	t.Run("should drop a comment that matches an existing bot comment by file+line+body-prefix", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{FilePath: "internal/foo.go", Line: 42, Body: "[high] this could be nil-checked"},
			},
		}
		newComments := []entities.ReviewComment{
			{FilePath: "internal/foo.go", Line: 42, Body: "[high] this could be nil-checked", Severity: "high"},
			{FilePath: "internal/foo.go", Line: 99, Body: "[medium] consider extracting", Severity: "medium"},
		}

		// when
		kept := commands.DropDuplicateComments(rc, context.Background(), provider, repo, prID, newComments)

		// then
		require.Len(t, kept, 1)
		assert.Equal(t, 99, kept[0].Line, "the new comment on a different line must be kept")
	})

	t.Run("should keep PR-wide comments (Line <= 0) regardless of duplicates", func(t *testing.T) {
		t.Parallel()

		// given: PR-wide annotations are not subject to the dedup pass —
		// the F2 review-once gate above already suppresses entire
		// follow-up reviews, so the only path that lands a duplicate
		// PR-wide comment is the explicit @code-guru re-review which the
		// user asked for.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{Body: "summary"},
			},
		}
		newComments := []entities.ReviewComment{
			{Line: 0, Body: "summary", Severity: "comment"},
		}

		// when
		kept := commands.DropDuplicateComments(rc, context.Background(), provider, repo, prID, newComments)

		// then
		assert.Len(t, kept, 1, "PR-wide comments must always pass through the inline-only dedup")
	})

	t.Run("should normalize the leading slash on file paths", func(t *testing.T) {
		t.Parallel()

		// given: ADO's threads carry `filePath: "/internal/foo.go"`
		// while the AI emits `internal/foo.go` — both must dedup.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{FilePath: "/internal/foo.go", Line: 42, Body: "[high] nil-check"},
			},
		}
		newComments := []entities.ReviewComment{
			{FilePath: "internal/foo.go", Line: 42, Body: "[high] nil-check", Severity: "high"},
		}

		// when
		kept := commands.DropDuplicateComments(rc, context.Background(), provider, repo, prID, newComments)

		// then
		assert.Empty(t, kept, "leading-slash normalisation must let the dedup match")
	})
}

func TestExecuteReviewOnceGate(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "feat", URL: "https://example/pr/4242",
	}

	t.Run(
		"should skip when an existing review-complete marker is present and the user has not mentioned the bot",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{
				existingComments: []forgeEntities.PullRequestComment{
					{Body: "✅ **Code Guru review complete.** Verdict: `approve`"},
				},
			}

			// when
			result, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{})

			// then
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, "comment", result.Verdict)
			assert.Contains(t, result.Summary, "already been reviewed")
			assert.Empty(t, provider.calls, "no marker / annotation should fire when the gate skips the review")
			assert.Empty(t, provider.submissions, "no native review should fire when the gate skips the review")
		},
	)

	t.Run(
		"should NOT skip when UserMentioned is true even with an existing review-complete marker",
		func(t *testing.T) {
			t.Parallel()

			// given: same precondition as the previous row plus a user
			// mention. The deterministic GetPullRequestFiles error proves
			// Execute reached past the gate (the user-requested re-review
			// went through).
			expectedErr := errors.New("get pull request files failed")
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{
				existingComments: []forgeEntities.PullRequestComment{
					{Body: "✅ **Code Guru review complete.** Verdict: `approve`"},
				},
				getPullRequestFilesErr: expectedErr,
			}

			// when
			result, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				UserMentioned: true,
			})

			// then
			require.Error(t, err)
			require.ErrorIs(t, err, expectedErr,
				"UserMentioned=true must bypass the review-once gate so the re-review reaches GetPullRequestFiles")
			assert.Nil(t, result)
		},
	)

	t.Run("should proceed when no marker is present", func(t *testing.T) {
		t.Parallel()

		// given: empty existingComments + the deterministic error
		// proves Execute walked past the gate down the normal path.
		expectedErr := errors.New("get pull request files failed")
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			getPullRequestFilesErr: expectedErr,
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{})

		// then
		require.Error(t, err)
		assert.ErrorIs(t, err, expectedErr)
	})
}

func TestBuildConversation(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242

	t.Run("should return nil when UserMentioned is false", func(t *testing.T) {
		t.Parallel()

		// given: a PR with several existing bot threads, but the run
		// was push-triggered (no mention). The conversation walk must
		// stay nil so first-pass review prompts are byte-for-byte
		// identical to the pre-conversation shape.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{ID: 1, Line: 10, FilePath: "internal/foo.go", Body: "[high] x", Author: "code-guru[bot]"},
			},
		}

		// when
		got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
			commands.ReviewOptions{UserMentioned: false})

		// then
		assert.Nil(t, got)
	})

	t.Run("should populate the conversation when UserMentioned is true", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{ID: 1, Line: 10, FilePath: "internal/foo.go", Body: "[high] nil-check", Author: "code-guru[bot]"},
				{
					ID:          2,
					Line:        10,
					FilePath:    "internal/foo.go",
					Body:        "we already handle that",
					Author:      "alice",
					InReplyToID: 1,
				},
			},
		}

		// when
		got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
			commands.ReviewOptions{UserMentioned: true})

		// then
		require.Len(t, got, 1)
		assert.Equal(t, "internal/foo.go", got[0].FilePath)
		assert.Equal(t, 10, got[0].Line)
		require.Len(t, got[0].Comments, 2)
		assert.Equal(t, "code-guru[bot]", got[0].Comments[0].Author)
		assert.Equal(t, "alice", got[0].Comments[1].Author)
	})

	t.Run("should soft-fail to nil when ListPullRequestComments errors", func(t *testing.T) {
		t.Parallel()

		// given: contract is best-effort — a list error must not break
		// the re-review; the F3 dedup catches any duplicates the LLM
		// emits without context.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			listCommentsErr: errors.New("transient API blip"),
		}

		// when
		got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
			commands.ReviewOptions{UserMentioned: true})

		// then
		assert.Nil(t, got, "list error must degrade to nil so the re-review still runs")
	})

	t.Run(
		"should keep threads anchored to files outside the live diff so the LLM can mark them outdated",
		func(t *testing.T) {
			t.Parallel()

			// given: bot has prior comments on both a file in the current
			// diff AND a file the PR no longer touches. Both must reach the
			// prompt — the stale-file thread is precisely the case the
			// `outdated` resolution status exists for, and dropping it at
			// the conversation stage would deny the LLM the chance to
			// auto-close it.
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{
				existingComments: []forgeEntities.PullRequestComment{
					{ID: 1, Line: 10, FilePath: "internal/foo.go", Body: "[high] live", Author: "code-guru[bot]"},
					{
						ID:       2,
						Line:     20,
						FilePath: "internal/old.go",
						Body:     "[high] stale anchor",
						Author:   "code-guru[bot]",
					},
				},
			}

			// when
			got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
				commands.ReviewOptions{UserMentioned: true})

			// then
			require.Len(
				t,
				got,
				2,
				"every prior bot thread must reach the LLM regardless of whether its file is still in the latest diff — that is what enables the `outdated` resolution path",
			)
			paths := []string{got[0].FilePath, got[1].FilePath}
			assert.Contains(t, paths, "internal/foo.go")
			assert.Contains(
				t,
				paths,
				"internal/old.go",
				"the stale-file thread must reach the prompt so the LLM can classify it as `outdated`; if it never appears in the conversation, the bot can never auto-close it",
			)
		},
	)

	t.Run("should recognise the bot under a custom service account via self-detection", func(t *testing.T) {
		t.Parallel()

		// given: a deployment that posts under a service account whose
		// name does NOT start with `code-guru` (so the built-in matcher
		// alone would miss it). The bot's PR-wide status annotation
		// carries the marker, an inline finding + the author's reply sit
		// on the same anchor, and NO identity is configured. Without
		// self-detection this returned nil and the LLM re-reviewed from
		// scratch, re-posting findings the author already answered.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{
					ID:     1,
					Line:   0,
					Author: "automation@example.com",
					Body:   "✅ **Code Guru review complete.**\n\nVerdict: `request_changes`.",
				},
				{
					ID:       2,
					Line:     10,
					FilePath: "internal/foo.go",
					Author:   "automation@example.com",
					Body:     "[high] this YAML value must be quoted",
				},
				{
					ID:          3,
					Line:        10,
					FilePath:    "internal/foo.go",
					Author:      "alice",
					Body:        "this file is auto-generated; the quoting cannot be configured",
					InReplyToID: 2,
				},
			},
		}

		// when: no BotIdentities supplied — pure self-detection.
		got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
			commands.ReviewOptions{UserMentioned: true})

		// then
		require.Len(
			t,
			got,
			1,
			"the bot's own thread must be recognised even when it posts under a non-`code-guru` account",
		)
		assert.Equal(t, "internal/foo.go", got[0].FilePath)
		require.Len(
			t,
			got[0].Comments,
			2,
			"the author's reply must be carried so the LLM can judge the correction instead of re-posting",
		)
		assert.Equal(t, "automation@example.com", got[0].Comments[0].Author)
		assert.Equal(t, "alice", got[0].Comments[1].Author)
	})

	t.Run("should recognise the bot via an explicitly configured BotIdentity", func(t *testing.T) {
		t.Parallel()

		// given: the bot's inline thread exists but there is NO PR-wide
		// marker annotation to self-detect from (e.g. the completion
		// notice was deleted). An explicitly configured identity must
		// still let the walk recognise the bot's thread.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			existingComments: []forgeEntities.PullRequestComment{
				{
					ID:       1,
					Line:     10,
					FilePath: "internal/foo.go",
					Author:   "automation@example.com",
					Body:     "[high] nil-check",
				},
				{
					ID:          2,
					Line:        10,
					FilePath:    "internal/foo.go",
					Author:      "alice",
					Body:        "fixed in latest push",
					InReplyToID: 1,
				},
			},
		}

		// when
		got := commands.BuildConversation(rc, context.Background(), provider, repo, prID,
			commands.ReviewOptions{UserMentioned: true, BotIdentities: []string{"automation@example.com"}})

		// then
		require.Len(t, got, 1)
		require.Len(t, got[0].Comments, 2)
		assert.Equal(t, "automation@example.com", got[0].Comments[0].Author)
		assert.Equal(t, "alice", got[0].Comments[1].Author)
	})
}

// recordingRegistry is a TrivialDetectorRegistry that always returns
// Detected=true with a fixed verdict, and records the file paths it
// receives so the test can pin the leading-`/` normalisation contract.
type recordingRegistry struct {
	verdict   string
	lastFiles []string
}

func (r *recordingRegistry) Detect(
	_ context.Context,
	dctx repositories.DetectionContext,
) (repositories.TrivialDetector, repositories.DetectionResult, bool) {
	r.lastFiles = append([]string(nil), dctx.Files...)
	d := &stubNamedDetector{name: "stub"}
	return d, repositories.DetectionResult{
		Detected: true,
		Verdict:  r.verdict,
		Summary:  "trivial",
	}, true
}

type stubNamedDetector struct{ name string }

func (s *stubNamedDetector) Name() string { return s.name }
func (s *stubNamedDetector) Detect(_ context.Context, _ repositories.DetectionContext) repositories.DetectionResult {
	return repositories.DetectionResult{}
}

// failingAIReviewer panics if its ReviewDiff is invoked. The trivial
// path must short-circuit before reaching it; if Execute somehow falls
// through to the AI call, the panic surfaces the regression cleanly.
type failingAIReviewer struct{}

func (failingAIReviewer) Name() string { return "fail" }
func (failingAIReviewer) ReviewDiff(_ context.Context, _ entities.ReviewRequest) (*entities.ReviewResult, error) {
	panic("AI review must not run when a trivial detector matches")
}

func TestExecuteRunsTrivialDetectionRegardlessOfCIPassed(t *testing.T) {
	t.Parallel()

	// Pins the no-CI-gate contract: trivial detection (e.g. docs-only,
	// bump-go) MUST fire even when CIPassed is false. Before this test
	// existed the gate at review_command.go suppressed every detector
	// because no entry point ever set CIPassed=true in production.

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "docs", URL: "https://example/pr/4242",
	}

	t.Run("should return the trivial verdict when CIPassed is false", func(t *testing.T) {
		t.Parallel()

		// given: an ADO-shape leading-`/` path (Azure DevOps's
		// `GetPullRequestFiles` prefixes one onto every path); a
		// registry that always detects + records the paths it sees;
		// an AI that panics if reached; a non-nil rules repo so a
		// future regression that disables the trivial path produces
		// the AI panic — not a `nil rulesRepo` panic that would
		// confuse the failure mode. CIPassed left at its zero value.
		registry := &recordingRegistry{verdict: "approve"}
		rules := &doubles.StubRulesRepository{}
		rc := commands.NewReviewCommand(failingAIReviewer{}, rules, registry, nil)
		provider := &recordingReviewProvider{
			files: []forgeEntities.PullRequestFile{{Path: "/CHANGELOG.md"}},
		}

		// when
		result, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			DryRun: true,
		})

		// then
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Equal(t, "approve", result.Verdict,
			"Execute must propagate the trivial detector's verdict even with CIPassed=false")
		assert.Equal(t, "trivial", result.Summary)
		assert.Equal(
			t,
			[]string{"CHANGELOG.md"},
			registry.lastFiles,
			"the leading `/` Azure DevOps prefixes onto every path must be stripped before the detector sees it — otherwise bump detectors miss their required-files match against `CHANGELOG.md`",
		)
	})
}

func TestTrivialFastPathPostsSingleMarkerAndOptionalMerge(t *testing.T) {
	t.Parallel()

	// Pins the trivial-path post / merge contract surfaced by smoke
	// an internal smoke PR: two pods posted four duplicate
	// approvals between them because the trivial path's old
	// `[Auto-Approved]` body did not contain `**Code Guru review`,
	// so the F2 review-once gate did not catch the second ADO
	// `pullrequest.updated` delivery. Native review submission also
	// echoed the body, doubling every review.
	//
	// New contract:
	//   - One PR-wide comment containing the F2 marker.
	//   - Native review submission carries an empty body so it
	//     does not duplicate the annotation.
	//   - `TrivialAutoMerge=true` triggers `MergePullRequest`;
	//     `false` (or a non-approve verdict) does not.

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "docs", URL: "https://example/pr/4242",
	}

	newCmd := func(verdict string) (*commands.ReviewCommand, *recordingReviewProvider) {
		registry := &recordingRegistry{verdict: verdict}
		rules := &doubles.StubRulesRepository{}
		rc := commands.NewReviewCommand(failingAIReviewer{}, rules, registry, nil)
		provider := &recordingReviewProvider{
			files: []forgeEntities.PullRequestFile{{Path: "/CHANGELOG.md"}},
		}
		return rc, provider
	}

	t.Run(
		"should post exactly one PR-wide comment carrying the `**Code Guru review` F2 marker on approve",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				SubmitNativeReview: true,
			})

			// then
			require.NoError(t, err)
			require.Len(
				t,
				provider.calls,
				1,
				"exactly one PR-wide comment must be posted on a trivial-approve verdict — duplicates flooded the smoke PR before this contract pinned it",
			)
			assert.Contains(
				t,
				provider.calls[0].body,
				"**Code Guru review",
				"the trivial-path comment MUST contain the F2 review-once-gate marker substring; without it the second ADO `pullrequest.updated` delivery re-runs the trivial path and posts again",
			)
			assert.NotContains(
				t,
				provider.calls[0].body,
				"[Auto-Approved]",
				"the legacy `[Auto-Approved]` prefix has been replaced by the unified completion annotation — its presence would mean the dedup contract regressed",
			)
		},
	)

	t.Run(
		"should submit a native review with empty body (vote-only) so it does not duplicate the annotation",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				SubmitNativeReview: true,
			})

			// then
			require.NoError(t, err)
			require.Len(t, provider.submissions, 1, "native review submission still records the reviewer-panel vote")
			assert.Empty(
				t,
				provider.submissions[0].Body,
				"the native submission's body MUST be empty so gitforge does not post the trivial summary as a second PR-wide comment alongside the annotation",
			)
		},
	)

	t.Run(
		"should call MergePullRequest with the configured strategy and NO bypass when only TrivialAutoMerge is set",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialAutoMerge:     true,
				TrivialMergeStrategy: "squash",
			})

			// then
			require.NoError(t, err)
			require.Len(
				t,
				provider.merges,
				1,
				"auto-merge must fire on a trivial-approve verdict when the operator opted in",
			)
			assert.Equal(t, 4242, provider.merges[0].prID)
			assert.Equal(
				t,
				"squash",
				provider.merges[0].strategy,
				"the configured merge strategy must reach gitforge unchanged — empty falls back to the platform default, but `squash` is an explicit operator choice",
			)
			assert.False(
				t,
				provider.merges[0].bypassPolicy,
				"TrivialAutoMerge alone MUST default to polite-merge: bypass requires the bot to hold the platform-level `Bypass policies when completing pull requests` permission, so flipping bypass on by default would turn previously-working auto-merges into hard 403s in environments where the bot has merge but not bypass permission",
			)
		},
	)

	t.Run(
		"should call MergePullRequest with bypass-policy when both TrivialAutoMerge and TrivialBypassPolicy are set",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialAutoMerge:     true,
				TrivialMergeStrategy: "rebaseMerge",
				TrivialBypassPolicy:  true,
			})

			// then
			require.NoError(t, err)
			require.Len(t, provider.merges, 1)
			assert.Equal(t, "rebaseMerge", provider.merges[0].strategy)
			assert.True(
				t,
				provider.merges[0].bypassPolicy,
				"TrivialBypassPolicy=true MUST forward gitforge.WithBypassPolicy so the merge call carries `bypassPolicy=true` — required for repos with `Required reviewers` policies that the bot itself cannot satisfy",
			)
			assert.NotEmpty(
				t,
				provider.merges[0].bypassReason,
				"the bypass reason MUST be non-empty so it lands in the ADO audit trail (ADO rejects empty `bypassReason` strings)",
			)
		},
	)

	t.Run(
		"should call MergePullRequest with delete-source-branch when TrivialAutoMerge and TrivialDeleteSourceBranch are set",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialAutoMerge:          true,
				TrivialMergeStrategy:      "squash",
				TrivialDeleteSourceBranch: true,
			})

			// then
			require.NoError(t, err)
			require.Len(t, provider.merges, 1)
			assert.True(
				t,
				provider.merges[0].deleteSourceBranch,
				"TrivialDeleteSourceBranch=true MUST forward gitforge.WithDeleteSourceBranch so the source branch is removed after the auto-merge completes",
			)
		},
	)

	t.Run(
		"should NOT pass delete-source-branch when TrivialDeleteSourceBranch is false even though TrivialAutoMerge fires",
		func(t *testing.T) {
			t.Parallel()

			// given: an operator who set `delete_source_branch: false` wants the
			// branch kept after the merge, so the option must not leak through.
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialAutoMerge:          true,
				TrivialDeleteSourceBranch: false,
			})

			// then
			require.NoError(t, err)
			require.Len(t, provider.merges, 1, "the merge still fires; only the branch-deletion cleanup is suppressed")
			assert.False(
				t,
				provider.merges[0].deleteSourceBranch,
				"TrivialDeleteSourceBranch=false MUST NOT forward WithDeleteSourceBranch, so an operator opt-out keeps the source branch",
			)
		},
	)

	t.Run(
		"should NOT pass delete-source-branch when TrivialDeleteSourceBranch is set without TrivialAutoMerge (the merge call never fires)",
		func(t *testing.T) {
			t.Parallel()

			// given: like bypass, delete-source-branch is meaningless without a
			// merge to attach it to.
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialDeleteSourceBranch: true,
			})

			// then
			require.NoError(t, err)
			assert.Empty(t, provider.merges,
				"delete-source-branch alone is a no-op — the gate that fires MergePullRequest is TrivialAutoMerge")
		},
	)

	t.Run(
		"should NOT pass bypass-policy when TrivialBypassPolicy is set without TrivialAutoMerge (the merge call never fires)",
		func(t *testing.T) {
			t.Parallel()

			// given: TrivialBypassPolicy without TrivialAutoMerge is a
			// no-op because there is no merge call to apply the option to.
			rc, provider := newCmd("approve")

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				TrivialBypassPolicy: true,
			})

			// then
			require.NoError(t, err)
			assert.Empty(
				t,
				provider.merges,
				"TrivialBypassPolicy alone is a no-op — the gate that fires `MergePullRequest` is `TrivialAutoMerge`, so without it the bypass setting has nothing to apply against",
			)
		},
	)

	t.Run("should NOT call MergePullRequest when TrivialAutoMerge=false (the default)", func(t *testing.T) {
		t.Parallel()

		// given
		rc, provider := newCmd("approve")

		// when: TrivialAutoMerge omitted — defaults to false
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{})

		// then
		require.NoError(t, err)
		assert.Empty(
			t,
			provider.merges,
			"auto-merge is opt-in by design; the default config must NEVER complete a PR cross-system without operator consent",
		)
	})

	t.Run("should NOT call MergePullRequest when verdict=reject even with TrivialAutoMerge=true", func(t *testing.T) {
		t.Parallel()

		// given
		rc, provider := newCmd("reject")

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			TrivialAutoMerge: true,
		})

		// then
		require.NoError(t, err)
		assert.Empty(
			t,
			provider.merges,
			"auto-merge fires only on the approve verdict — a trivial detector that rejects (e.g. an incomplete bump per `.autobump.yaml`) must never auto-merge",
		)
	})

	t.Run("should auto-merge when the PR author is in the allowlist", func(t *testing.T) {
		t.Parallel()

		// given: a trivial-approve PR opened by a trusted automation
		// account that the operator allow-listed.
		rc, provider := newCmd("approve")
		botPR := forgeEntities.PullRequestDetail{
			ID: 4242, Title: "deps", URL: "https://example/pr/4242",
			Author: "automation@example.com",
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, botPR, commands.ReviewOptions{
			TrivialAutoMerge:        true,
			TrivialAutoMergeAuthors: []string{"automation@example.com"},
		})

		// then
		require.NoError(t, err)
		require.Len(t, provider.merges, 1,
			"a trivial-approve PR from an allow-listed automation author must auto-merge")
	})

	t.Run(
		"should NOT auto-merge when the PR author is not in the allowlist (e.g. a human docs PR)",
		func(t *testing.T) {
			t.Parallel()

			// given: same trivial-approve verdict, but the PR was opened by a
			// human whose account is NOT in the allowlist. Triviality makes it
			// eligible; the allowlist withholds the unattended merge so a human
			// still merges it — the whole point of this gate.
			rc, provider := newCmd("approve")
			humanPR := forgeEntities.PullRequestDetail{
				ID: 4242, Title: "docs", URL: "https://example/pr/4242",
				Author: "alice@example.com",
			}

			// when
			_, err := rc.Execute(context.Background(), provider, repo, humanPR, commands.ReviewOptions{
				SubmitNativeReview:      true,
				TrivialAutoMerge:        true,
				TrivialBypassPolicy:     true,
				TrivialAutoMergeAuthors: []string{"automation@example.com"},
			})

			// then
			require.NoError(t, err)
			assert.Empty(
				t,
				provider.merges,
				"a trivial PR from a non-allow-listed (human) author MUST NOT be auto-merged even with bypass on — it is approved and left for a human to merge",
			)
			require.Len(t, provider.submissions, 1,
				"the PR is still reviewed and voted on; only the merge is withheld")
		},
	)

	t.Run("should match the author allowlist case-insensitively", func(t *testing.T) {
		t.Parallel()

		// given: the allowlist entry differs only in case from the PR's
		// author (account identities are not case-sensitive).
		rc, provider := newCmd("approve")
		botPR := forgeEntities.PullRequestDetail{
			ID: 4242, Title: "deps", URL: "https://example/pr/4242",
			Author: "Automation@Example.com",
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, botPR, commands.ReviewOptions{
			TrivialAutoMerge:        true,
			TrivialAutoMergeAuthors: []string{"automation@example.com"},
		})

		// then
		require.NoError(t, err)
		require.Len(
			t,
			provider.merges,
			1,
			"author matching must be case-insensitive so a casing difference between config and the provider's author string does not silently disable auto-merge",
		)
	})
}

func TestExecuteLLMPathSubmitsNativeReviewWithEmptyBody(t *testing.T) {
	t.Parallel()

	// Pins the no-duplicate-summary contract for the LLM path. The bot
	// posts the rationale exactly once, inside the
	// `postReviewCompleteAnnotation` body produced by
	// `buildReviewCompleteBody`. The native review submission MUST carry
	// an empty body so it does NOT echo the same summary as a second
	// PR-wide comment on Azure DevOps. Surfaced live: the LLM path was
	// posting two PR-wide comments per review, the first being just
	// `result.Summary` (the native submission echo) and the second being
	// the full annotation that ALSO contained `result.Summary` — the
	// same content twice, on top of each other.

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "feat", URL: "https://example/pr/4242",
	}

	t.Run("should submit native review with empty body and let the annotation carry the summary", func(t *testing.T) {
		t.Parallel()

		// given: a stub AI reviewer returning a result with a non-empty
		// `Summary`. The trivial registry is nil (so detection short-
		// circuits and the LLM path runs); rules are stubbed empty.
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result: &entities.ReviewResult{
				Verdict: "request_changes",
				Summary: "Three blocking issues found in the diff.",
			},
		}
		rc := commands.NewReviewCommand(ai, rules, nil, nil)
		provider := &recordingReviewProvider{
			// Non-empty Patch keeps the executor on the per-file diff
			// path and avoids the ADO `GetPullRequestDiff` fallback,
			// which the recording provider does not stub.
			files: []forgeEntities.PullRequestFile{
				{Path: "internal/foo.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
			},
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			SubmitNativeReview: true,
		})

		// then
		require.NoError(t, err)
		require.Len(t, provider.submissions, 1, "native review submission still records the reviewer-panel vote")
		assert.Empty(
			t,
			provider.submissions[0].Body,
			"the native submission's body MUST be empty so gitforge does not post the LLM summary as a second PR-wide comment alongside the completion annotation. Without this contract, every LLM review left a duplicate summary on the PR.",
		)

		// also: the annotation body MUST still contain the summary so
		// the rationale is visible exactly once.
		var annotationBody string
		summaryOccurrences := 0
		for _, c := range provider.calls {
			if strings.Contains(c.body, "Code Guru review complete") {
				annotationBody = c.body
			}
			if strings.Contains(c.body, ai.Result.Summary) {
				summaryOccurrences++
			}
		}
		require.NotEmpty(t, annotationBody, "the completion annotation must still be posted")
		assert.Contains(
			t,
			annotationBody,
			ai.Result.Summary,
			"the annotation MUST carry the LLM summary so the rationale is visible — without this the empty-body native submission would erase the rationale entirely",
		)
		assert.Equal(
			t,
			1,
			summaryOccurrences,
			"the LLM summary text MUST appear in exactly ONE PR-wide comment (the annotation). The standalone summary post that `postComments` used to emit on no-inline-comments reviews is removed in favour of letting the annotation be the single source of truth — without this gate we'd drift back into the duplicate-summary failure mode the live PR surfaced.",
		)
	})
}

// TestApplyThreadResolutions pins the resolution-aware re-review
// contract introduced by the `feat/resolution-aware-rereview` work:
// instead of re-emitting the same finding (or reworded duplicate) on
// every `@code-guru` mention, the bot now classifies every prior bot
// thread as `resolved` / `outstanding` / `outdated` and acts on each
// classification surgically — one reply per thread, plus an auto-close
// for `resolved` / `outdated` so the PR author does not have to dismiss
// addressed threads by hand.
func TestApplyThreadResolutions(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242

	// fixedThreads is the conversation block the walker would have
	// produced from the prior bot inline comments. Each thread carries
	// the gitforge ThreadID so the auto-close path has a concrete
	// integer to forward to UpdatePullRequestThreadStatus.
	fixedThreads := []entities.ReviewThread{
		{
			FilePath:      "internal/foo.go",
			Line:          10,
			ThreadID:      111,
			RootCommentID: 1,
			Comments: []entities.ReviewMessage{
				{Author: "code-guru[bot]", Body: "[high] possible nil deref"},
			},
		},
		{
			FilePath:      "internal/bar.go",
			Line:          20,
			ThreadID:      222,
			RootCommentID: 2,
			Comments: []entities.ReviewMessage{
				{Author: "code-guru[bot]", Body: "[medium] avoid global state"},
			},
		},
	}

	t.Run("should post one reply per resolution and update status for resolved threads", func(t *testing.T) {
		t.Parallel()

		// given: the LLM classifies thread #111 as resolved (the diff
		// fixed it) and thread #222 as outstanding (still present).
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		resolutions := []entities.ThreadResolution{
			{FilePath: "internal/foo.go", Line: 10, Status: "resolved", Explanation: "Diff adds the nil check."},
			{
				FilePath:    "internal/bar.go",
				Line:        20,
				Status:      "outstanding",
				Explanation: "Latest diff still references the global.",
			},
		}

		// when
		handled := commands.ApplyThreadResolutions(
			rc,
			context.Background(),
			provider,
			repo,
			prID,
			fixedThreads,
			resolutions,
		)

		// then: one reply per resolution, each anchored on the original
		// thread's file:line so the user sees the bot engaging with the
		// existing thread instead of opening a parallel comment.
		require.Len(
			t,
			provider.replies,
			2,
			"there must be exactly one reply per ThreadResolution — duplicate replies would re-create the flooding the resolution path is designed to fix",
		)
		assert.Empty(
			t,
			provider.threadComments,
			"a thread with a usable ThreadID must be replied to IN-THREAD (ReplyToThread), never as a new same-line comment — that fragmentation is what this feature removes",
		)
		assert.Equal(t, 111, provider.replies[0].threadID,
			"the reply must nest in the resolved thread (#111), not a new thread on the same line")
		assert.Contains(
			t,
			provider.replies[0].body,
			"Resolved",
			"the resolved-reply body must carry the explicit Resolved headline so the PR author can tell at a glance the bot considers this addressed",
		)
		assert.Contains(
			t,
			provider.replies[0].body,
			"Diff adds the nil check.",
			"the LLM's explanation must surface in the reply body — without it the user has the verdict but no rationale",
		)
		assert.Equal(t, 222, provider.replies[1].threadID,
			"the outstanding reply must nest in thread #222")
		assert.Contains(t, provider.replies[1].body, "outstanding",
			"the outstanding-reply body must mention `outstanding` so the user knows the concern is still open")

		// only the `resolved` thread should auto-close — `outstanding`
		// keeps the thread `active` since the bot is restating the
		// concern, not closing the loop.
		require.Len(
			t,
			provider.threadStatusUpdates,
			1,
			"only the resolved/outdated resolutions should call UpdatePullRequestThreadStatus; outstanding leaves the thread active",
		)
		assert.Equal(t, 111, provider.threadStatusUpdates[0].threadID,
			"the auto-close must target the resolved thread, not the outstanding one")
		assert.Equal(
			t,
			"fixed",
			provider.threadStatusUpdates[0].status,
			"`resolved` must map to the platform `fixed` state — that is what ADO renders as a closed-with-resolution thread",
		)

		// the returned anchor set must only carry CLOSED-thread
		// anchors — `outstanding` keeps the prior thread active, and a
		// new comment on the same line is more likely a separate
		// finding than a duplicate, so suppressing it would be more
		// aggressive than the duplicate-guard the dedup gate is meant
		// to be. With `resolved` + `outstanding` here, only the
		// `resolved` anchor (`internal/foo.go:10`) must be in the set.
		require.Len(
			t,
			handled,
			1,
			"the dedup gate must drop new comments only on anchors whose prior thread is now closed; outstanding anchors must remain free for distinct new findings",
		)
		_, ok := handled["internal/foo.go:10"]
		assert.True(
			t,
			ok,
			"the resolved-status anchor must be in the dedup gate so a duplicate of the same finding gets suppressed",
		)
	})

	t.Run("should map outdated to closed and auto-close the thread", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		resolutions := []entities.ThreadResolution{
			{
				FilePath:    "internal/foo.go",
				Line:        10,
				Status:      "outdated",
				Explanation: "The function in question was deleted.",
			},
		}

		// when
		handled := commands.ApplyThreadResolutions(
			rc,
			context.Background(),
			provider,
			repo,
			prID,
			fixedThreads,
			resolutions,
		)

		// then
		require.Len(t, provider.replies, 1)
		assert.Contains(t, provider.replies[0].body, "Outdated",
			"the outdated-reply body must mention `Outdated` so the user knows the concern no longer applies")
		require.Len(t, provider.threadStatusUpdates, 1)
		assert.Equal(t, "closed", provider.threadStatusUpdates[0].status,
			"`outdated` must map to the platform `closed` state — soft-close without making a content claim")
		assert.Len(t, handled, 1)
	})

	t.Run("should disambiguate duplicate anchors via the synthetic prompt id", func(t *testing.T) {
		t.Parallel()

		// given: two prior bot threads share the same `<file>:<line>`
		// anchor — the failure mode this fix addresses. Without the
		// synthetic id, the post-pipeline's anchor map would collapse
		// both threads onto one entry and silently lose every
		// resolution past the first. With the id, each resolution
		// routes to the correct thread.
		twoThreadsSameAnchor := []entities.ReviewThread{
			{
				FilePath: "internal/foo.go", Line: 10, ThreadID: 111, RootCommentID: 1,
				Comments: []entities.ReviewMessage{{Author: "code-guru[bot]", Body: "[high] nil-check"}},
			},
			{
				FilePath:      "internal/foo.go",
				Line:          10,
				ThreadID:      222,
				RootCommentID: 5,
				Comments: []entities.ReviewMessage{
					{Author: "code-guru[bot]", Body: "[medium] separate concern on the same line"},
				},
			},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		resolutions := []entities.ThreadResolution{
			{
				ID:          "T1",
				FilePath:    "internal/foo.go",
				Line:        10,
				Status:      "resolved",
				Explanation: "Diff added the nil check.",
			},
			{
				ID:          "T2",
				FilePath:    "internal/foo.go",
				Line:        10,
				Status:      "outdated",
				Explanation: "The unrelated concern no longer applies.",
			},
		}

		// when
		handled := commands.ApplyThreadResolutions(
			rc,
			context.Background(),
			provider,
			repo,
			prID,
			twoThreadsSameAnchor,
			resolutions,
		)

		// then: BOTH resolutions must route to their correct thread —
		// the auto-close calls must hit ThreadID 111 (T1, resolved →
		// fixed) and ThreadID 222 (T2, outdated → closed). Without the
		// id-based match the post-pipeline would silently drop one of
		// these.
		require.Len(t, provider.replies, 2,
			"each prior thread sharing the anchor must receive its own reply when the LLM disambiguates via id")
		require.Len(
			t,
			provider.threadStatusUpdates,
			2,
			"both closing resolutions must trigger an UpdatePullRequestThreadStatus call — without the id-based match one would be silently dropped",
		)
		statusByThreadID := map[int]string{}
		for _, u := range provider.threadStatusUpdates {
			statusByThreadID[u.threadID] = u.status
		}
		assert.Equal(t, "fixed", statusByThreadID[111],
			"T1 → resolved must map ThreadID 111 to `fixed`")
		assert.Equal(
			t,
			"closed",
			statusByThreadID[222],
			"T2 → outdated must map ThreadID 222 to `closed` — without the id-based match this would have routed to ThreadID 111 (a wrong-thread auto-close) or been dropped",
		)
		assert.Len(
			t,
			handled,
			1,
			"both resolutions close the same anchor `internal/foo.go:10`, so the dedup gate has one normalised key (the set, not the list)",
		)
	})

	t.Run("should normalise leading slash so ADO-shape paths match conversation anchors", func(t *testing.T) {
		t.Parallel()

		// given: the LLM emits the unprefixed path (matching the prompt's
		// `Thread T1 on internal/foo.go:10` header) but the conversation walker
		// captured the ADO-shape `/internal/foo.go`. Both must compare equal
		// after normalisation, otherwise every ADO re-review would skip every
		// resolution as "unmatched".
		threadsADO := []entities.ReviewThread{
			{
				FilePath: "/internal/foo.go", Line: 10, ThreadID: 111, RootCommentID: 1,
				Comments: []entities.ReviewMessage{{Author: "code-guru[bot]", Body: "x"}},
			},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		resolutions := []entities.ThreadResolution{
			{FilePath: "internal/foo.go", Line: 10, Status: "resolved", Explanation: "Done."},
		}

		// when
		handled := commands.ApplyThreadResolutions(
			rc,
			context.Background(),
			provider,
			repo,
			prID,
			threadsADO,
			resolutions,
		)

		// then
		require.Len(
			t,
			provider.replies,
			1,
			"ADO/AI path normalisation must let the resolution match the conversation thread",
		)
		assert.Len(t, handled, 1)
	})

	t.Run(
		"should skip a resolution whose anchor matches no prior thread (LLM hallucinated anchor)",
		func(t *testing.T) {
			t.Parallel()

			// given: the LLM emits a resolution for a file:line the prompt
			// never showed it. Without this guard, the bot would post an
			// inline reply on a random line and call status updates on a
			// thread that does not exist.
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &recordingReviewProvider{}
			resolutions := []entities.ThreadResolution{
				{FilePath: "does/not/exist.go", Line: 99, Status: "resolved", Explanation: "."},
			}

			// when
			handled := commands.ApplyThreadResolutions(
				rc,
				context.Background(),
				provider,
				repo,
				prID,
				fixedThreads,
				resolutions,
			)

			// then
			assert.Empty(t, provider.replies, "an unmatched resolution must not reply on any thread")
			assert.Empty(
				t,
				provider.threadComments,
				"an unmatched resolution must not produce a stray inline comment somewhere on the PR",
			)
			assert.Empty(
				t,
				provider.threadStatusUpdates,
				"an unmatched resolution must not attempt a status update on a thread that does not exist",
			)
			assert.Empty(t, handled, "no anchor was handled, so the dedup gate must remain empty")
		},
	)

	t.Run("should skip the auto-close when the thread has no usable ThreadID", func(t *testing.T) {
		t.Parallel()

		// given: GitHub today does not always carry a thread-status
		// concept; gitforge surfaces ThreadID=0 in that case. The
		// reply still posts (the user benefits from the explanation),
		// but the auto-close call is skipped because there is no thread
		// for the provider to update.
		threadsNoID := []entities.ReviewThread{
			{
				FilePath: "internal/foo.go", Line: 10, ThreadID: 0, RootCommentID: 1,
				Comments: []entities.ReviewMessage{{Author: "code-guru[bot]", Body: "x"}},
			},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}
		resolutions := []entities.ThreadResolution{
			{FilePath: "internal/foo.go", Line: 10, Status: "resolved", Explanation: "Done."},
		}

		// when
		commands.ApplyThreadResolutions(rc, context.Background(), provider, repo, prID, threadsNoID, resolutions)

		// then
		require.Len(t, provider.threadComments, 1,
			"with no usable ThreadID the reply must FALL BACK to a fresh inline comment at the anchor (not be dropped)")
		assert.Empty(t, provider.replies,
			"ThreadID 0 has no thread to reply into, so the in-thread ReplyToThread path must NOT be used")
		assert.Empty(
			t,
			provider.threadStatusUpdates,
			"a ThreadID of 0 must NOT trigger an UpdatePullRequestThreadStatus call — the provider has no handle to act on",
		)
	})

	t.Run("should return nil and skip when there are no resolutions", func(t *testing.T) {
		t.Parallel()

		// given
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}

		// when
		handled := commands.ApplyThreadResolutions(rc, context.Background(), provider, repo, prID, fixedThreads, nil)

		// then
		assert.Nil(t, handled)
		assert.Empty(t, provider.replies)
		assert.Empty(t, provider.threadComments)
		assert.Empty(t, provider.threadStatusUpdates)
	})

	t.Run("should still mark the anchor handled even when the reply post fails", func(t *testing.T) {
		t.Parallel()

		// given: the soft-fail contract — a reply failure must still
		// suppress the duplicate inline comment in `postComments` on the
		// same anchor. Otherwise a transient ADO blip would let the bot
		// post the comment that the resolution was supposed to replace.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{
			replyErr: errors.New("transient ADO 503"),
		}
		resolutions := []entities.ThreadResolution{
			{FilePath: "internal/foo.go", Line: 10, Status: "resolved", Explanation: "Done."},
		}

		// when
		handled := commands.ApplyThreadResolutions(
			rc,
			context.Background(),
			provider,
			repo,
			prID,
			fixedThreads,
			resolutions,
		)

		// then
		require.Len(
			t,
			provider.replies,
			1,
			"the helper attempted the in-thread reply once before the error path took over",
		)
		assert.Empty(
			t,
			provider.threadStatusUpdates,
			"a reply failure must short-circuit the auto-close so the bot does not advertise a `fixed` thread that has no visible reply",
		)
		assert.Len(
			t,
			handled,
			1,
			"the anchor must be marked handled even on reply failure so the surrounding postComments still drops the duplicate inline comment",
		)
	})
}

// TestExecuteMentionPathAppliesThreadResolutions wires the LLM stub to a
// recording provider end-to-end so the resolution-aware re-review path
// is exercised through Execute, not just through the helper. This is
// the test that fails if a future refactor disconnects ThreadResolutions
// from the post-pipeline (e.g. someone forgets to forward it through a
// new wrapper) — the helper-level tests above would still pass even
// then.
func TestExecuteMentionPathAppliesThreadResolutions(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "feat", URL: "https://example/pr/4242",
	}

	t.Run(
		"should reply on each prior thread, auto-close the resolved one, and drop the duplicated new comment",
		func(t *testing.T) {
			t.Parallel()

			// given:
			//   * one prior bot inline thread on internal/foo.go:10 (ThreadID 111).
			//   * the LLM marks that thread `resolved` AND emits a new
			//     comment on the same anchor — the duplicate-flood failure
			//     mode this whole change is fixing. The dedup-by-anchor
			//     gate must drop that new comment so the thread does NOT
			//     receive both a "Resolved" reply AND a duplicate "still
			//     not handled" inline comment.
			rules := &doubles.StubRulesRepository{}
			ai := &doubles.StubAIReviewerRepository{
				NameValue: "stub",
				Result: &entities.ReviewResult{
					Verdict: "approve",
					Summary: "All prior issues addressed.",
					ThreadResolutions: []entities.ThreadResolution{
						{
							FilePath:    "internal/foo.go",
							Line:        10,
							Status:      "resolved",
							Explanation: "The new diff guards against nil before deref.",
						},
					},
					Comments: []entities.ReviewComment{
						{
							FilePath: "internal/foo.go",
							Line:     10,
							Severity: "warning",
							Body:     "Reworded restatement of the prior nil-check finding.",
						},
					},
				},
			}
			rc := commands.NewReviewCommand(ai, rules, nil, nil)
			provider := &recordingReviewProvider{
				files: []forgeEntities.PullRequestFile{
					{Path: "internal/foo.go", Patch: "@@ -10 +10 @@\n-old\n+new\n"},
				},
				existingComments: []forgeEntities.PullRequestComment{
					{
						ID:       1,
						ThreadID: 111,
						Line:     10,
						FilePath: "internal/foo.go",
						Body:     "[high] consider nil-check",
						Author:   "code-guru[bot]",
					},
				},
			}

			// when
			_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
				UserMentioned: true,
			})

			// then
			require.NoError(t, err)

			// 1. exactly one inline reply on the prior thread's anchor —
			//    the bot engaged with the existing thread instead of
			//    flooding the PR with a parallel comment.
			require.Len(
				t,
				provider.replies,
				1,
				"the mention path must produce exactly one reply per prior thread; the new `comments[]` entry on the same anchor must NOT also produce an inline post — that is the duplicate-flood failure mode",
			)
			assert.Equal(
				t,
				111,
				provider.replies[0].threadID,
				"the verdict must nest IN the prior thread (#111), not as a new same-line comment that confuses the author",
			)
			assert.Contains(
				t,
				provider.replies[0].body,
				"Resolved",
				"the resolved-status reply must surface the green-check headline so the user sees the bot considers the prior concern addressed",
			)

			// 2. the resolved thread must auto-close so the user does not
			//    have to dismiss it by hand.
			require.Len(
				t,
				provider.threadStatusUpdates,
				1,
				"a resolved status must call UpdatePullRequestThreadStatus so the platform thread state matches the bot's verdict",
			)
			assert.Equal(t, 111, provider.threadStatusUpdates[0].threadID)
			assert.Equal(t, "fixed", provider.threadStatusUpdates[0].status)

			// 3. the LLM's prompt must have received the conversation —
			//    pin the contract so a future refactor that disconnects
			//    BuildConversation from ReviewRequest fails here.
			require.Len(
				t,
				ai.LastRequest.Conversation,
				1,
				"the LLM must receive the prior thread as conversation context — that is what lets it judge whether the concern is resolved",
			)
			assert.Equal(t, int64(111), ai.LastRequest.Conversation[0].ThreadID,
				"the gitforge ThreadID must propagate from BuildConversation through ReviewRequest into the LLM call")
		},
	)
}

// TestBuildResolutionReplyBody pins the body shape per LLM verdict so a
// future formatting refactor cannot silently turn the headline into
// something the PR author cannot scan.
func TestBuildResolutionReplyBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		status       string
		explanation  string
		wantHeadline string
	}{
		{
			name:         "resolved headline carries the green check",
			status:       "resolved",
			explanation:  "Fixed.",
			wantHeadline: "Resolved",
		},
		{
			name:         "outstanding headline carries the warn",
			status:       "outstanding",
			explanation:  "Still here.",
			wantHeadline: "Still outstanding",
		},
		{
			name:         "outdated headline carries the soft close",
			status:       "outdated",
			explanation:  "Code removed.",
			wantHeadline: "Outdated",
		},
		{
			name:         "unknown status falls back to a generic note",
			status:       "weird",
			explanation:  "?",
			wantHeadline: "Code Guru re-review note",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// given
			res := entities.ThreadResolution{Status: tc.status, Explanation: tc.explanation}

			// when
			body := commands.BuildResolutionReplyBody(res)

			// then
			assert.Contains(t, body, tc.wantHeadline,
				"every resolution status must produce a headline the PR author can scan without reading the body")
			assert.Contains(t, body, tc.explanation,
				"the LLM's explanation must surface in the body verbatim — that is the rationale the user reads")
		})
	}

	t.Run("should fall back to a placeholder when the explanation is empty", func(t *testing.T) {
		t.Parallel()

		// given: the prompt asks for an explanation but malformed
		// responses sometimes drop it. The body must still render
		// something readable rather than a trailing blank section.
		res := entities.ThreadResolution{Status: "resolved", Explanation: ""}

		// when
		body := commands.BuildResolutionReplyBody(res)

		// then
		assert.Contains(t, body, "(no explanation provided)",
			"an empty explanation must surface a placeholder so the body is never just a bare headline")
	})
}

// TestMapResolutionStatusToThreadState pins the LLM-vocabulary →
// platform-vocabulary mapping the auto-close path forwards to gitforge.
// A regression here would silently change how resolved threads render
// on Azure DevOps (`fixed` vs. `closed` vs. `active`).
func TestMapResolutionStatusToThreadState(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status string
		want   string
	}{
		{name: "resolved maps to fixed", status: "resolved", want: "fixed"},
		{name: "outdated maps to closed", status: "outdated", want: "closed"},
		{name: "outstanding leaves the thread active", status: "outstanding", want: "active"},
		{name: "case-insensitive: RESOLVED still maps to fixed", status: "RESOLVED", want: "fixed"},
		{name: "trims whitespace before mapping", status: "  resolved  ", want: "fixed"},
		{
			name:   "unknown verbiage falls back to active so the bot never auto-closes by accident",
			status: "weird",
			want:   "active",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// given / when
			got := commands.MapResolutionStatusToThreadState(tc.status)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestShouldCloseResolution pins which LLM verdicts trigger the
// auto-close path. `outstanding` must NOT close the thread — the bot is
// restating the concern, not signalling resolution.
func TestShouldCloseResolution(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "resolved closes", status: "resolved", want: true},
		{name: "outdated closes", status: "outdated", want: true},
		{name: "outstanding does NOT close", status: "outstanding", want: false},
		{name: "case-insensitive resolved still closes", status: "Resolved", want: true},
		{name: "unknown verbiage does NOT close (defensive)", status: "approve", want: false},
		{name: "empty does NOT close", status: "", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// given / when
			got := commands.ShouldCloseResolution(tc.status)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

// fileAccessRecordingProvider extends recordingReviewProvider with the
// gitforge FileAccessProvider surface so tests can drive the project-
// guidelines (CLAUDE.md) fetch. Only GetFileContent / HasFile are
// implemented; the embedded nil forgeEntities.FileAccessProvider
// supplies the rest of that interface's method set (ListFiles, GetTags,
// CreateBranchWithChanges), which panics if reached — the same
// panic-on-unexpected-call posture recordingReviewProvider documents.
type fileAccessRecordingProvider struct {
	recordingReviewProvider
	forgeEntities.FileAccessProvider

	// fileContents seeds GetFileContent / HasFile responses by path.
	fileContents map[string]string
	// fileErr, when set, makes GetFileContent fail so tests can pin the
	// best-effort contract (a fetch failure never fails the review).
	fileErr error
	// fetchedPaths records every GetFileContent call so tests can assert
	// the loader's skip gates really skip the network call, not just the
	// returned value.
	fetchedPaths []string
}

func (p *fileAccessRecordingProvider) GetFileContent(
	_ context.Context,
	_ forgeEntities.Repository,
	path string,
) (string, error) {
	p.fetchedPaths = append(p.fetchedPaths, path)
	if p.fileErr != nil {
		return "", p.fileErr
	}
	content, ok := p.fileContents[path]
	if !ok {
		return "", fmt.Errorf("file %q not found in stub", path)
	}
	return content, nil
}

func (p *fileAccessRecordingProvider) HasFile(
	_ context.Context,
	_ forgeEntities.Repository,
	path string,
) bool {
	_, ok := p.fileContents[path]
	return ok
}

func TestLoadProjectGuidelines(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242
	enabled := commands.ReviewOptions{LoadProjectGuidelines: true}

	t.Run(
		"should fetch the repository CLAUDE.md when enabled and the provider supports file access",
		func(t *testing.T) {
			t.Parallel()

			// given
			rc := commands.NewReviewCommand(nil, nil, nil, nil)
			provider := &fileAccessRecordingProvider{
				fileContents: map[string]string{"CLAUDE.md": "# Project rules\n\nUse BDD blocks in every test.\n"},
			}

			// when
			got := commands.LoadProjectGuidelines(
				rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

			// then
			assert.Equal(t, "# Project rules\n\nUse BDD blocks in every test.", got,
				"the fetched content must be returned trimmed so the prompt does not carry stray blank lines")
			assert.Equal(t, []string{"CLAUDE.md"}, provider.fetchedPaths,
				"exactly one fetch for the root CLAUDE.md must be issued")
		},
	)

	t.Run("should return empty and never fetch when the option is disabled", func(t *testing.T) {
		t.Parallel()

		// given: operator set `ai.project_guidelines: false` — the wire
		// from settings resolves to LoadProjectGuidelines=false.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": "# Project rules"},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"},
			commands.ReviewOptions{LoadProjectGuidelines: false})

		// then
		assert.Empty(t, got)
		assert.Empty(t, provider.fetchedPaths,
			"the opt-out must skip the provider call entirely, not just discard the result")
	})

	t.Run("should skip the fetch when the PR diff already touches CLAUDE.md", func(t *testing.T) {
		t.Parallel()

		// given: the PR modifies the guidelines file itself. The diff
		// already shows it to the model; fetching the pre-change copy on
		// top would present two conflicting versions of the document.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": "# stale pre-change copy"},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"CLAUDE.md", "internal/foo.go"}, enabled)

		// then
		assert.Empty(t, got)
		assert.Empty(t, provider.fetchedPaths,
			"a CLAUDE.md-touching diff must skip the provider call — the model reads the file in the diff")
	})

	t.Run("should skip the fetch for the ADO-shape /CLAUDE.md path too", func(t *testing.T) {
		t.Parallel()

		// given: Azure DevOps prefixes every changed path with `/`. The
		// skip gate must normalise before comparing, mirroring the rule
		// used across the rest of the pipeline.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": "# stale pre-change copy"},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"/CLAUDE.md"}, enabled)

		// then
		assert.Empty(t, got)
		assert.Empty(t, provider.fetchedPaths)
	})

	t.Run("should return empty when the provider does not support file access", func(t *testing.T) {
		t.Parallel()

		// given: a provider without the FileAccessProvider surface —
		// nothing to fetch from, and the review must proceed regardless.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &recordingReviewProvider{}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

		// then
		assert.Empty(t, got)
	})

	t.Run("should return empty when the fetch fails so the review proceeds without guidelines", func(t *testing.T) {
		t.Parallel()

		// given: a transient provider outage (or simply a repository with
		// no CLAUDE.md — gitforge surfaces both as an error). Best-effort
		// by contract: the loader degrades to "no guidelines".
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{fileErr: errors.New("503 Service Unavailable")}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

		// then
		assert.Empty(t, got)
		assert.Len(t, provider.fetchedPaths, 1, "the loader must have attempted exactly one fetch before degrading")
	})

	t.Run("should return empty when the file is whitespace-only", func(t *testing.T) {
		t.Parallel()

		// given: an effectively-empty CLAUDE.md must not add an empty
		// guidelines section to the prompt.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": "  \n\t\n"},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

		// then
		assert.Empty(t, got)
	})

	t.Run("should truncate oversized guidelines at the byte cap with the sentinel", func(t *testing.T) {
		t.Parallel()

		// given: a pathological guidelines file larger than the cap. The
		// bound is applied at load time so every backend sees the same
		// bounded content and the diff keeps its share of the context
		// window.
		oversized := strings.Repeat("A", commands.DefaultMaxProjectGuidelinesBytes+1024)
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": oversized},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

		// then
		assert.Len(t, got, commands.DefaultMaxProjectGuidelinesBytes+len("...[truncated]"),
			"the content must be cut at the cap plus the truncation sentinel")
		assert.True(t, strings.HasSuffix(got, "...[truncated]"),
			"the sentinel must close the content so the model can tell the document was cut")
	})

	t.Run("should send a real-world guidelines file whole under the default budget", func(t *testing.T) {
		t.Parallel()

		// given: the regression this budget exists to prevent. A large but
		// entirely legitimate CLAUDE.md (256 KiB — well beyond the 32 KiB
		// the loader used to allow) must reach the model INTACT: judging a
		// diff against a document that stops mid-sentence is worse than a
		// long prompt, because the model silently applies half a standard.
		wholeDocument := strings.Repeat("B", 256*1024)
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": wholeDocument},
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, enabled)

		// then
		assert.Equal(t, wholeDocument, got,
			"a legitimate guidelines file well under the budget must not be truncated at all")
		assert.NotContains(t, got, "...[truncated]")
	})

	t.Run("should honour an operator-configured budget below the default", func(t *testing.T) {
		t.Parallel()

		// given: a deployment on a small-context-window backend lowers the
		// budget so a huge guidelines file cannot crowd out the diff.
		// The explicit value must win over the shipped default.
		const operatorBudget = 4096
		content := strings.Repeat("C", operatorBudget*4)
		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": content},
		}
		bounded := commands.ReviewOptions{
			LoadProjectGuidelines: true,
			MaxGuidelinesBytes:    operatorBudget,
		}

		// when
		got := commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, bounded)

		// then
		assert.Len(t, got, operatorBudget+len("...[truncated]"),
			"the configured budget must override the default, not be ignored")
	})
}

// TestLoadProjectGuidelinesTruncationWarning pins the exact boundary the
// warning condition must fire on. It is deliberately NOT parallel: it
// captures entries off the global logger via a local hook, matching the
// established pattern in `internal/support/response_parser_test.go`.
//
// The regression it guards: `support.Truncate` appends a 14-byte sentinel,
// so a document just a few bytes over budget produces a `bounded` value
// LONGER than the original. A `len(bounded) < len(trimmed)` guard (the
// first cut of this code) therefore stays silent for every file in the
// (budget, budget+sentinel] band — truncating the project's conventions
// without ever telling the operator. The condition must key off the
// ORIGINAL length vs. the budget instead.
func TestLoadProjectGuidelinesTruncationWarning(t *testing.T) {
	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242
	const budget = 4096

	warned := func(t *testing.T, contentLen int) bool {
		t.Helper()

		// given: a guidelines file of exactly contentLen bytes and a hook
		// capturing the global logger for the duration of the load.
		hook := logrustest.NewLocal(logger.StandardLogger())
		defer hook.Reset()

		rc := commands.NewReviewCommand(nil, nil, nil, nil)
		provider := &fileAccessRecordingProvider{
			fileContents: map[string]string{"CLAUDE.md": strings.Repeat("D", contentLen)},
		}
		opts := commands.ReviewOptions{LoadProjectGuidelines: true, MaxGuidelinesBytes: budget}

		// when
		commands.LoadProjectGuidelines(
			rc, context.Background(), provider, repo, prID, []string{"internal/foo.go"}, opts)

		// then
		for _, entry := range hook.AllEntries() {
			if entry.Level == logger.WarnLevel && strings.Contains(entry.Message, "TRUNCATED") {
				return true
			}
		}
		return false
	}

	t.Run("should warn when the file is just over budget (inside the sentinel band)", func(t *testing.T) {
		// given: budget+5 — with the old len(bounded) < len(trimmed) guard,
		// bounded (budget+14) is LONGER than trimmed (budget+5), so no warn
		// fired even though the content was cut.
		assert.True(t, warned(t, budget+5),
			"a document a few bytes over budget is still truncated and must warn")
	})

	t.Run("should warn when the file is far over budget", func(t *testing.T) {
		// given
		assert.True(t, warned(t, budget*4),
			"an obviously oversized document must warn")
	})

	t.Run("should NOT warn when the file exactly fills the budget", func(t *testing.T) {
		// given: len == budget is not a truncation (support.Truncate cuts
		// only when len > n), so no warning must be emitted.
		assert.False(t, warned(t, budget),
			"a document that exactly fits must not warn — nothing was cut")
	})
}

func TestDiffTouchesProjectGuidelines(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		paths []string
		want  bool
	}{
		{name: "should match the root CLAUDE.md", paths: []string{"CLAUDE.md"}, want: true},
		{name: "should match the ADO-shape /CLAUDE.md", paths: []string{"/CLAUDE.md"}, want: true},
		{name: "should match case-insensitively (claude.md)", paths: []string{"claude.md"}, want: true},
		{
			name:  "should NOT match a nested docs/CLAUDE.md — only the root file is the repository's guidance",
			paths: []string{"docs/CLAUDE.md"},
			want:  false,
		},
		{name: "should NOT match a suffixed CLAUDE.md.bak", paths: []string{"CLAUDE.md.bak"}, want: false},
		{name: "should return false on an empty path list", paths: nil, want: false},
		{
			name:  "should return false when no changed path is the guidelines file",
			paths: []string{"internal/foo.go", "README.md"},
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// when
			got := commands.DiffTouchesProjectGuidelines(tc.paths)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestExecuteLLMPathLoadsProjectGuidelines(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "feat", URL: "https://example/pr/4242",
	}

	t.Run("should forward the fetched CLAUDE.md to the AI request", func(t *testing.T) {
		t.Parallel()

		// given: a repository carrying a CLAUDE.md and a provider with
		// file access — the end-to-end wiring the feature exists for.
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result:    &entities.ReviewResult{Verdict: "approve"},
		}
		rc := commands.NewReviewCommand(ai, rules, nil, nil)
		provider := &fileAccessRecordingProvider{
			files: []forgeEntities.PullRequestFile{
				{Path: "internal/foo.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
			},
			fileContents: map[string]string{"CLAUDE.md": "# Conventions\n\nAlways alias logrus as logger."},
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			LoadProjectGuidelines: true,
		})

		// then
		require.NoError(t, err)
		assert.Equal(t, "# Conventions\n\nAlways alias logrus as logger.", ai.LastRequest.ProjectGuidelines,
			"the reviewed repository's CLAUDE.md must reach the AI request so every backend renders it into the prompt")
		assert.Equal(t, []string{"CLAUDE.md"}, provider.fetchedPaths)
	})

	t.Run("should leave ProjectGuidelines empty when the operator disabled the feature", func(t *testing.T) {
		t.Parallel()

		// given
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result:    &entities.ReviewResult{Verdict: "approve"},
		}
		rc := commands.NewReviewCommand(ai, rules, nil, nil)
		provider := &fileAccessRecordingProvider{
			files: []forgeEntities.PullRequestFile{
				{Path: "internal/foo.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"},
			},
			fileContents: map[string]string{"CLAUDE.md": "# Conventions"},
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			LoadProjectGuidelines: false,
		})

		// then
		require.NoError(t, err)
		assert.Empty(t, ai.LastRequest.ProjectGuidelines)
		assert.Empty(t, provider.fetchedPaths, "the opt-out must not issue the file-content call at all")
	})

	t.Run("should not fetch when the PR itself modifies CLAUDE.md", func(t *testing.T) {
		t.Parallel()

		// given: the diff carries the guidelines change — the model reads
		// it there, and the repository fetch (which would return the
		// default-branch copy) is skipped.
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result:    &entities.ReviewResult{Verdict: "approve"},
		}
		rc := commands.NewReviewCommand(ai, rules, nil, nil)
		provider := &fileAccessRecordingProvider{
			files: []forgeEntities.PullRequestFile{
				{Path: "CLAUDE.md", Patch: "@@ -1 +1 @@\n-old guidance\n+new guidance\n"},
			},
			fileContents: map[string]string{"CLAUDE.md": "# default-branch copy"},
		}

		// when
		_, err := rc.Execute(context.Background(), provider, repo, pr, commands.ReviewOptions{
			LoadProjectGuidelines: true,
		})

		// then
		require.NoError(t, err)
		assert.Empty(t, ai.LastRequest.ProjectGuidelines,
			"the pre-change copy must not be layered on top of the diff that rewrites it")
		assert.Empty(t, provider.fetchedPaths)
	})
}

// TestLoadPullRequestMetadata pins the gates and hygiene of the PR
// metadata loader: the operator opt-out and the nil-repository wiring
// must skip the fetch entirely, a fetch error must degrade to the zero
// value, and the description must arrive trimmed and bounded so a
// generated changelog-dump body cannot blow the prompt budget.
func TestLoadPullRequestMetadata(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	const prID = 4242
	enabled := commands.ReviewOptions{LoadPullRequestMetadata: true}

	t.Run("should return the fetched metadata with a trimmed description when enabled", func(t *testing.T) {
		t.Parallel()

		// given
		stub := &doubles.StubPullRequestMetadataRepository{
			Metadata: entities.PullRequestMetadata{
				Description: "  Adds a rate limiter.\n\n",
				CommitCount: 3,
			},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, stub)

		// when
		got := commands.LoadPullRequestMetadata(
			rc, context.Background(), &recordingReviewProvider{}, repo, prID, enabled)

		// then
		assert.Equal(t, "Adds a rate limiter.", got.Description,
			"the description must be trimmed so the prompt does not carry stray blank lines")
		assert.Equal(t, 3, got.CommitCount)
		assert.Equal(t, 1, stub.Calls)
		assert.Equal(t, prID, stub.LastPRID)
	})

	t.Run("should return zero and never fetch when the option is disabled", func(t *testing.T) {
		t.Parallel()

		// given: operator set `ai.pr_metadata: false` — the wire from
		// settings resolves to LoadPullRequestMetadata=false.
		stub := &doubles.StubPullRequestMetadataRepository{
			Metadata: entities.PullRequestMetadata{Description: "unused", CommitCount: 9},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, stub)

		// when
		got := commands.LoadPullRequestMetadata(
			rc, context.Background(), &recordingReviewProvider{}, repo, prID,
			commands.ReviewOptions{LoadPullRequestMetadata: false})

		// then
		assert.Zero(t, got)
		assert.Zero(t, stub.Calls,
			"the opt-out must skip the repository call entirely, not just discard the result")
	})

	t.Run("should return zero when no metadata repository is wired", func(t *testing.T) {
		t.Parallel()

		// given: paths that build the command without the fetcher (e.g.
		// hand-rolled test wiring) must behave exactly as before the
		// feature existed.
		rc := commands.NewReviewCommand(nil, nil, nil, nil)

		// when
		got := commands.LoadPullRequestMetadata(
			rc, context.Background(), &recordingReviewProvider{}, repo, prID, enabled)

		// then
		assert.Zero(t, got)
	})

	t.Run("should return zero when the fetch fails so the review proceeds without metadata", func(t *testing.T) {
		t.Parallel()

		// given: an unsupported provider or a transient API outage —
		// best-effort by contract, the loader degrades to "no metadata".
		stub := &doubles.StubPullRequestMetadataRepository{Err: errors.New("503 Service Unavailable")}
		rc := commands.NewReviewCommand(nil, nil, nil, stub)

		// when
		got := commands.LoadPullRequestMetadata(
			rc, context.Background(), &recordingReviewProvider{}, repo, prID, enabled)

		// then
		assert.Zero(t, got)
		assert.Equal(t, 1, stub.Calls, "the loader must have attempted exactly one fetch before degrading")
	})

	t.Run("should truncate an oversized description at the documented bound", func(t *testing.T) {
		t.Parallel()

		// given: release bots paste entire upstream changelogs into PR
		// bodies; the loader must bound them before the prompt is built.
		oversized := strings.Repeat("x", commands.DefaultMaxPRDescriptionBytes+100)
		stub := &doubles.StubPullRequestMetadataRepository{
			Metadata: entities.PullRequestMetadata{Description: oversized},
		}
		rc := commands.NewReviewCommand(nil, nil, nil, stub)

		// when
		got := commands.LoadPullRequestMetadata(
			rc, context.Background(), &recordingReviewProvider{}, repo, prID, enabled)

		// then
		assert.Less(t, len(got.Description), len(oversized))
		assert.Contains(t, got.Description, "...[truncated]",
			"the cut must carry the sentinel so the model knows the document was bounded, not complete")
	})
}

// TestExecuteForwardsPullRequestMetadata pins the end-to-end wiring: a
// review run with the option enabled must surface the fetched metadata
// on the AI request, and a metadata fetch failure must not fail the
// review.
func TestExecuteForwardsPullRequestMetadata(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 7, Title: "Add limiter",
		SourceBranch: "feat/limiter",
		TargetBranch: "main",
	}
	newProvider := func() *recordingReviewProvider {
		return &recordingReviewProvider{
			files: []forgeEntities.PullRequestFile{
				{Path: "main.go", Patch: "@@ -1 +1 @@\n-a\n+b\n"},
			},
		}
	}

	t.Run("should forward the fetched metadata to the AI request", func(t *testing.T) {
		t.Parallel()

		// given
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result:    &entities.ReviewResult{Verdict: "approve"},
		}
		metadata := &doubles.StubPullRequestMetadataRepository{
			Metadata: entities.PullRequestMetadata{Description: "Adds a limiter.", CommitCount: 2},
		}
		rc := commands.NewReviewCommand(ai, rules, nil, metadata)

		// when
		_, err := rc.Execute(context.Background(), newProvider(), repo, pr, commands.ReviewOptions{
			LoadPullRequestMetadata: true,
		})

		// then
		require.NoError(t, err)
		assert.Equal(t, "Adds a limiter.", ai.LastRequest.Metadata.Description)
		assert.Equal(t, 2, ai.LastRequest.Metadata.CommitCount)
	})

	t.Run("should complete the review with zero metadata when the fetch fails", func(t *testing.T) {
		t.Parallel()

		// given
		rules := &doubles.StubRulesRepository{}
		ai := &doubles.StubAIReviewerRepository{
			NameValue: "stub",
			Result:    &entities.ReviewResult{Verdict: "approve"},
		}
		metadata := &doubles.StubPullRequestMetadataRepository{Err: errors.New("boom")}
		rc := commands.NewReviewCommand(ai, rules, nil, metadata)

		// when
		result, err := rc.Execute(context.Background(), newProvider(), repo, pr, commands.ReviewOptions{
			LoadPullRequestMetadata: true,
		})

		// then
		require.NoError(t, err, "a metadata fetch failure must never fail the review")
		require.NotNil(t, result)
		assert.Zero(t, ai.LastRequest.Metadata)
	})
}
