package claude_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	claude "github.com/rios0rios0/codeguru/internal/infrastructure/repositories/claude"
	"github.com/rios0rios0/codeguru/internal/support"
	entitybuilders "github.com/rios0rios0/codeguru/test/domain/entitybuilders"
)

// writeFakeClaudeBinary drops a tiny `/bin/sh` script in `t.TempDir()`
// that mimics the real Claude CLI failure surface: it consumes stdin,
// optionally prints `$FAKE_STDOUT` to fd 1 and `$FAKE_STDERR` to fd 2,
// then exits with `$FAKE_EXIT`. Tests configure the env vars via
// `t.Setenv` (which means they cannot run with `t.Parallel`, since
// process-wide env mutation conflicts with parallel siblings — that
// trade-off is acceptable here because the tests are fast and the
// regression they pin is critical: every claude crash since the
// repository was written has been logged as `(stderr: )` because the
// wrapper threw away stdout, and the fake binary is the only way to
// drive the real `ReviewDiff` end-to-end without an actual `claude`
// install.
func writeFakeClaudeBinary(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binary uses /bin/sh; not portable to Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude")
	body := `#!/bin/sh
cat > /dev/null
[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
[ -n "$FAKE_STDERR" ] && printf '%s' "$FAKE_STDERR" >&2
exit "${FAKE_EXIT:-0}"
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o755))
	return path
}

// minimalReviewRequest constructs a request that exercises the prompt
// build path without bloating the test diff. The diff value itself does
// not matter — the fake binary discards stdin — but the entity must be
// well-formed so the wrapper's prompt construction does not short-circuit.
func minimalReviewRequest() entities.ReviewRequest {
	return entities.ReviewRequest{
		PullRequest: forgeEntities.PullRequestDetail{
			PullRequest:  forgeEntities.PullRequest{Title: "feat: smoke"},
			SourceBranch: "feat/x",
			TargetBranch: "main",
		},
		Diffs: []entities.FileDiff{
			{Path: "main.go", Diff: "@@ -1 +1 @@\n-old\n+new\n", Language: "go"},
		},
	}
}

func TestClaudeReviewer_ReviewDiff_FailureCapturesBothStreams(t *testing.T) {
	// `t.Setenv` panics under `t.Parallel`, and we control the failure
	// shape via env, so this group runs serially. The trade-off is
	// pinned in the writeFakeClaudeBinary doc above.

	t.Run(
		"should include stdout in the error when claude exits non-zero with a JSON error envelope on stdout",
		func(t *testing.T) {
			// given: the canonical Anthropic CLI failure shape — a JSON
			// envelope on stdout (per `--output-format json`) plus a small
			// auxiliary message on stderr. Captured live across PRs
			// `#NNNN`, `#NNNN`, `#NNNN`, `#NNNN`, `#NNNN` on
			// `2026-05-01`, where every error line in production logs
			// showed `(stderr: )` because `AIReviewerRepository.ReviewDiff`
			// threw away the child process's stdout — hiding the only
			// diagnostic the CLI actually produced. Code-guru PR #98 ships
			// the fix; this test pins it.
			bin := writeFakeClaudeBinary(t)
			t.Setenv("FAKE_STDOUT", `{"error":"rate_limit_exceeded","message":"too many requests"}`)
			t.Setenv("FAKE_STDERR", "auxiliary stderr context")
			t.Setenv("FAKE_EXIT", "1")
			repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

			// when
			result, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

			// then
			require.Error(t, err)
			assert.Nil(t, result)
			errMsg := err.Error()
			assert.Contains(t, errMsg, "rate_limit_exceeded",
				"stdout payload must reach the operator log so the failure is debuggable")
			assert.Contains(t, errMsg, "auxiliary stderr context",
				"stderr payload must keep being captured (no regression)")
			assert.Contains(t, errMsg, "exit status 1",
				"the wrapped error must keep the underlying exec.ExitError text")
		},
	)

	t.Run("should include stderr alone when claude exits non-zero with no stdout output", func(t *testing.T) {
		// given: defensive — some claude failure modes only print to
		// stderr (e.g. invalid CLI args). The pre-existing stderr
		// capture must keep working; this is the negative-of-negative
		// test that would catch a regression in the new capture path.
		bin := writeFakeClaudeBinary(t)
		t.Setenv("FAKE_STDOUT", "")
		t.Setenv("FAKE_STDERR", "Error: --max-turns must be > 0")
		t.Setenv("FAKE_EXIT", "2")
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		_, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--max-turns must be > 0")
		assert.Contains(t, err.Error(), "exit status 2")
	})

	t.Run("should truncate captured streams to the documented cap so the error line stays bounded", func(t *testing.T) {
		// given: an oversized stdout (8 KB of `A`s, twice the 4 KB cap).
		// `support.TruncateBytesForLog` quotes the value with
		// `strconv.Quote` and ends with a `...[truncated]` sentinel;
		// pin both halves of the contract so a future "let me
		// un-truncate to make debugging easier" refactor surfaces in
		// the test before it floods the log pipeline.
		bin := writeFakeClaudeBinary(t)
		oversized := strings.Repeat("A", 8192)
		t.Setenv("FAKE_STDOUT", oversized)
		t.Setenv("FAKE_STDERR", "")
		t.Setenv("FAKE_EXIT", "1")
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		_, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

		// then
		require.Error(t, err)
		errMsg := err.Error()
		assert.Less(t, len(errMsg), 9000,
			"the error message must not echo the full 8 KB stdout — the truncation cap is the whole point")
		assert.Contains(t, errMsg, "AAAA",
			"some of the oversized stdout must still surface (truncate, not drop)")
		assert.Contains(t, errMsg, "...[truncated]",
			"the sentinel from support.TruncateBytesForLog must be appended to flag the cut")
	})

	t.Run("should classify a 'prompt is too long' CLI envelope as a context-window failure", func(t *testing.T) {
		// given: the Claude CLI wraps the Anthropic too-large 400 in its JSON
		// error envelope on stdout (per `--output-format json`). This is the
		// too-large failure class — it must carry the sentinel so the retry
		// decorator skips the (futile) re-sample and the PR gets "split your
		// PR" guidance instead of "usually transient".
		bin := writeFakeClaudeBinary(t)
		t.Setenv("FAKE_STDOUT",
			`{"type":"error","error":{"type":"invalid_request_error",`+
				`"message":"prompt is too long: 258000 tokens > 200000 maximum"}}`)
		t.Setenv("FAKE_STDERR", "")
		t.Setenv("FAKE_EXIT", "1")
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		_, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

		// then
		require.Error(t, err)
		assert.ErrorIs(t, err, support.ErrContextWindowExceeded,
			"the CLI's prompt-too-long envelope must carry the sentinel so retries are skipped")
	})
}

// writeArgvRecordingClaudeBinary drops a `/bin/sh` stub that appends each
// argument it received (one per line, so an EMPTY argument is recorded as an
// empty line) to `$FAKE_ARGV_FILE` before replying with `$FAKE_STDOUT`. It is
// the only way to pin the CLI invocation's argument vector without an actual
// `claude` install, and the empty-line fidelity matters: the flag under test
// (`--tools ""`) is asserted precisely by its empty value.
func writeArgvRecordingClaudeBinary(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake binary uses /bin/sh; not portable to Windows")
	}
	dir := t.TempDir()
	binPath := filepath.Join(dir, "fake-claude-argv")
	argvPath := filepath.Join(dir, "argv.txt")
	body := `#!/bin/sh
cat > /dev/null
for a in "$@"; do printf '%s\n' "$a" >> "$FAKE_ARGV_FILE"; done
[ -n "$FAKE_STDOUT" ] && printf '%s' "$FAKE_STDOUT"
exit 0
`
	require.NoError(t, os.WriteFile(binPath, []byte(body), 0o755))
	return binPath, argvPath
}

func TestClaudeReviewer_ReviewDiff_DisablesAllTools(t *testing.T) {
	// `t.Setenv` panics under `t.Parallel`; see writeFakeClaudeBinary's doc.

	t.Run("should pass an empty --tools so no tool is in the model's scope", func(t *testing.T) {
		// given: a review is a one-shot text completion. With tools in scope
		// the CLI runs its agentic loop and the model spends TURNS on tool
		// calls instead of emitting the review, exiting `error_max_turns`
		// with no review at all. `--disallowedTools` does not prevent this
		// (it blocks execution, not availability — the model still emits
		// `tool_use` and still burns the turns); only an empty `--tools`
		// removes the definitions. This test pins the flag that fixes it.
		bin, argvPath := writeArgvRecordingClaudeBinary(t)
		t.Setenv("FAKE_ARGV_FILE", argvPath)
		t.Setenv("FAKE_STDOUT", `{"result":"{\"summary\":\"ok\",\"comments\":[]}"}`)
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		result, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

		// then
		require.NoError(t, err)
		require.NotNil(t, result)

		recorded, readErr := os.ReadFile(argvPath)
		require.NoError(t, readErr)
		argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")

		toolsIdx := slices.Index(argv, "--tools")
		require.NotEqual(t, -1, toolsIdx,
			"the CLI must be invoked with --tools; without it the model keeps tools in scope")
		require.Less(t, toolsIdx+1, len(argv), "--tools must be followed by its value")
		assert.Empty(t, argv[toolsIdx+1],
			`--tools must receive an EMPTY value: that is what disables every built-in tool`)

		// `--tools` is variadic, so only a following `--`-prefixed flag
		// terminates its value list. If it were placed last it would swallow
		// nothing and the guarantee would silently vanish.
		promptIdx := slices.Index(argv, "--system-prompt-file")
		require.NotEqual(t, -1, promptIdx, "the system prompt flag must still be passed")
		assert.Less(t, toolsIdx, promptIdx,
			"--tools must precede a --flag that terminates its variadic value list")
	})
}

func TestClaudeReviewer_ReviewDiff_PassesSystemPromptThroughAFile(t *testing.T) {
	// `t.Setenv` panics under `t.Parallel`; see writeFakeClaudeBinary's doc.

	t.Run("should pass the system prompt via --system-prompt-file, never as an argument", func(t *testing.T) {
		// given: the system prompt embeds the operator's whole rule corpus.
		// Linux caps a SINGLE argv string at `MAX_ARG_STRLEN` (128 KiB) —
		// independent of `ARG_MAX` — so passing it inline made every review of
		// a multi-language pull request die at exec with "argument list too
		// long", before the model was reached. A rule corpus comfortably over
		// that cap is the regression this pins: it must review fine.
		bin, argvPath := writeArgvRecordingClaudeBinary(t)
		t.Setenv("FAKE_ARGV_FILE", argvPath)
		t.Setenv("FAKE_STDOUT", `{"result":"{\"summary\":\"ok\",\"comments\":[]}"}`)
		request := minimalReviewRequest()
		request.Rules = []entities.Rule{{
			Name:     "oversized",
			Category: "oversized",
			Content:  strings.Repeat("rule body line\n", 16384), // ~240 KB
		}}
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		result, err := repo.ReviewDiff(context.Background(), request)

		// then
		require.NoError(t, err,
			"a rule corpus past the OS per-argument limit must still review — that is the whole fix")
		require.NotNil(t, result)

		recorded, readErr := os.ReadFile(argvPath)
		require.NoError(t, readErr)
		argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")

		assert.Equal(t, -1, slices.Index(argv, "--system-prompt"),
			"the inline flag must NOT be used: it is what reintroduces the OS argument limit")
		fileIdx := slices.Index(argv, "--system-prompt-file")
		require.NotEqual(t, -1, fileIdx, "the system prompt must be handed over as a file path")
		require.Less(t, fileIdx+1, len(argv), "--system-prompt-file must be followed by its value")

		for _, arg := range argv {
			assert.Less(t, len(arg), 128*1024,
				"no single argument may approach the OS per-argument limit")
		}
	})

	t.Run("should remove the staged prompt file once the review returns", func(t *testing.T) {
		// given: the file carries the operator's rule corpus into a shared
		// `/tmp`. Leaving one behind per review would accumulate unbounded on a
		// long-running server, so cleanup is part of the contract, not hygiene.
		bin, argvPath := writeArgvRecordingClaudeBinary(t)
		t.Setenv("FAKE_ARGV_FILE", argvPath)
		t.Setenv("FAKE_STDOUT", `{"result":"{\"summary\":\"ok\",\"comments\":[]}"}`)
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		_, err := repo.ReviewDiff(context.Background(), minimalReviewRequest())

		// then
		require.NoError(t, err)
		recorded, readErr := os.ReadFile(argvPath)
		require.NoError(t, readErr)
		argv := strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
		fileIdx := slices.Index(argv, "--system-prompt-file")
		require.NotEqual(t, -1, fileIdx)

		_, statErr := os.Stat(argv[fileIdx+1])
		assert.ErrorIs(t, statErr, os.ErrNotExist,
			"the staged system prompt file must be removed after the CLI call returns")
	})

	t.Run("should classify an OS argument-limit refusal when the prompt file cannot be staged", func(t *testing.T) {
		// given: no writable temp dir, so the backend falls back to the inline
		// flag (preserving pre-fix behaviour for such a deployment), plus a
		// rule corpus past the OS limit so the fallback is actually refused.
		// The refusal must carry the sentinel: the kernel rejects the identical
		// exec every time, so retrying is futile and the PR needs the
		// operator-facing notice rather than "usually transient".
		bin := writeFakeClaudeBinary(t)
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
		t.Setenv("FAKE_EXIT", "0")
		request := minimalReviewRequest()
		request.Rules = []entities.Rule{{
			Name:     "oversized",
			Category: "oversized",
			Content:  strings.Repeat("rule body line\n", 16384), // ~240 KB
		}}
		repo := claude.NewAIReviewerRepository(bin, "sonnet", 1)

		// when
		result, err := repo.ReviewDiff(context.Background(), request)

		// then
		require.Error(t, err)
		assert.Nil(t, result)
		assert.ErrorIs(t, err, support.ErrArgumentListTooLong,
			"an exec refused for an oversized argument must carry the sentinel so retries are skipped")
	})
}

func TestParseClaudeResponse(t *testing.T) {
	t.Parallel()

	t.Run("should parse direct JSON from CLI output", func(t *testing.T) {
		t.Parallel()

		// given
		review := entitybuilders.NewReviewResultBuilder().
			WithSummary("looks good").
			WithComments([]entities.ReviewComment{
				entitybuilders.NewReviewCommentBuilder().
					WithFilePath("main.go").
					WithLine(10).
					WithBody("fix this").
					WithSeverity("error").
					BuildReviewComment(),
			}).
			BuildReviewResult()
		innerJSON, _ := json.Marshal(review)
		cliResp := map[string]string{"result": string(innerJSON)}
		output, _ := json.Marshal(cliResp)

		// when
		result, err := claude.ParseClaudeResponse(output)

		// then
		require.NoError(t, err)
		assert.Equal(t, "looks good", result.Summary)
		assert.Len(t, result.Comments, 1)
		assert.Equal(t, "main.go", result.Comments[0].FilePath)
	})

	t.Run("should parse JSON wrapped in markdown code fence", func(t *testing.T) {
		t.Parallel()

		// given
		review := `{"summary": "all good", "comments": []}`
		content := "Here is my review:\n```json\n" + review + "\n```\n"
		cliResp := map[string]string{"result": content}
		output, _ := json.Marshal(cliResp)

		// when
		result, err := claude.ParseClaudeResponse(output)

		// then
		require.NoError(t, err)
		assert.Equal(t, "all good", result.Summary)
		assert.Empty(t, result.Comments)
	})

	t.Run("should return ErrUnparseableResponse when CLI result is plain text", func(t *testing.T) {
		t.Parallel()

		// given: the parser refuses to fabricate a `Summary: content` result
		// because the command layer would otherwise post the raw model output
		// straight onto the PR. See `internal/support/response_parser.go`.
		content := "I couldn't find any issues with this PR."
		cliResp := map[string]string{"result": content}
		output, _ := json.Marshal(cliResp)

		// when
		result, err := claude.ParseClaudeResponse(output)

		// then
		require.Error(t, err)
		require.ErrorIs(t, err, support.ErrUnparseableResponse)
		assert.Nil(t, result)
	})

	t.Run("should repair unescaped quotes inside string values", func(t *testing.T) {
		t.Parallel()

		// given: the canonical LLM failure observed on
		// `internal/auth-service#NNNN` — the `body` field contains an
		// unescaped quoted phrase. The repair pass escapes the inner quotes
		// so the parse succeeds.
		innerJSON := `{"verdict":"comment","summary":"ok","comments":[{"file":"a.go","line":1,"severity":"info","body":"Rule: "Always escape quotes" applies"}]}`
		cliResp := map[string]string{"result": innerJSON}
		output, _ := json.Marshal(cliResp)

		// when
		result, err := claude.ParseClaudeResponse(output)

		// then
		require.NoError(t, err)
		require.Len(t, result.Comments, 1)
		assert.Equal(t, `Rule: "Always escape quotes" applies`, result.Comments[0].Body)
	})

	t.Run("should handle raw JSON output without CLI wrapper", func(t *testing.T) {
		t.Parallel()

		// given
		review := entitybuilders.NewReviewResultBuilder().
			WithSummary("raw output").
			WithComments([]entities.ReviewComment{
				entitybuilders.NewReviewCommentBuilder().
					WithFilePath("test.go").
					WithLine(5).
					WithBody("nit").
					WithSeverity("info").
					BuildReviewComment(),
			}).
			BuildReviewResult()
		output, _ := json.Marshal(review)

		// when
		result, err := claude.ParseClaudeResponse(output)

		// then
		require.NoError(t, err)
		assert.Equal(t, "raw output", result.Summary)
		assert.Len(t, result.Comments, 1)
	})
}
