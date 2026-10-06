package commands_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/domain/commands"
	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/support"
	doubles "github.com/rios0rios0/codeguru/test/domain/doubles/repositories"
)

// windowedAIReviewer is a hand-rolled `AIReviewerRepository` double that
// models the one backend behaviour the batching fallback exists for: a
// finite context window. Any request whose diff bytes exceed `windowBytes`
// is rejected with the same sentinel a real backend wraps its
// "prompt is too long" error in; anything smaller comes back as a review
// naming every file the request carried.
//
// Hand-rolled rather than driven by a mock library (banned by the testing
// standard) and deliberately behavioural rather than call-scripted: the
// batch planner's whole job is to discover a size that fits, so a double
// that answers by SIZE is what actually exercises it. A call-count script
// would pass even if the planner never shrank anything.
type windowedAIReviewer struct {
	windowBytes int
	// requests records every request the planner issued, in order, so a
	// test can assert coverage (every file reviewed exactly once), the
	// batch framing, and the conversation partitioning.
	requests []entities.ReviewRequest
	// successfulCalls counts only the calls that fit the window, so the
	// scripted responses below stay pinned to the batches the planner
	// actually reviewed. Indexing off `len(requests)` instead would shift
	// every scripted verdict by however many overflow probes the planner
	// happened to spend finding a size that fits — a test asserting on the
	// third batch's verdict would silently start asserting on the second.
	successfulCalls int
	// verdicts, when set, supplies the verdict for the Nth (1-based)
	// successful call; calls past the end fall back to `approve`.
	verdicts []string
	// summaries mirrors verdicts for the per-batch summary text.
	summaries []string
	// resolutions, when set, supplies the thread resolutions returned by
	// the Nth (1-based) successful call.
	resolutions [][]entities.ThreadResolution
	// failEveryRequest makes every call fail with a non-overflow error,
	// exercising the "batch errored" accounting.
	failEveryRequest error
}

func (r *windowedAIReviewer) Name() string { return "windowed" }

func (r *windowedAIReviewer) ReviewDiff(
	_ context.Context,
	request entities.ReviewRequest,
) (*entities.ReviewResult, error) {
	r.requests = append(r.requests, request)

	if r.failEveryRequest != nil {
		return nil, r.failEveryRequest
	}

	weight := 0
	for _, diff := range request.Diffs {
		weight += len(diff.Diff)
	}
	if weight > r.windowBytes {
		return nil, fmt.Errorf(
			"%w (windowed: prompt is too long: %d tokens > %d maximum)",
			support.ErrContextWindowExceeded, weight, r.windowBytes,
		)
	}

	r.successfulCalls++
	call := r.successfulCalls
	result := &entities.ReviewResult{Verdict: "approve"}
	if call <= len(r.verdicts) {
		result.Verdict = r.verdicts[call-1]
	}
	if call <= len(r.summaries) {
		result.Summary = r.summaries[call-1]
	}
	if call <= len(r.resolutions) {
		result.ThreadResolutions = r.resolutions[call-1]
	}
	for _, diff := range request.Diffs {
		result.Comments = append(result.Comments, entities.ReviewComment{
			FilePath: diff.Path,
			Line:     1,
			Body:     "reviewed " + diff.Path,
			Severity: "info",
		})
	}

	return result, nil
}

// nilResultAIReviewer returns `(nil, nil)` — a shape the repository
// contract does not forbid and a real backend could regress into. The
// batched run must degrade rather than dereference it.
type nilResultAIReviewer struct{}

func (r *nilResultAIReviewer) Name() string { return "nil-result" }

func (r *nilResultAIReviewer) ReviewDiff(
	_ context.Context,
	_ entities.ReviewRequest,
) (*entities.ReviewResult, error) {
	return nil, nil //nolint:nilnil // the point of the double is the degenerate return
}

// fileDiffs builds `count` files of `size` diff bytes each, named f0..fN.
func fileDiffs(count, size int) []entities.FileDiff {
	diffs := make([]entities.FileDiff, 0, count)
	for i := range count {
		diffs = append(diffs, entities.FileDiff{
			Path: fmt.Sprintf("pkg/f%d.go", i),
			Diff: strings.Repeat("x", size),
		})
	}

	return diffs
}

// reviewedPaths collects, in order, every file path the double was asked
// to review across all SUCCESSFUL calls (a request that overflowed is
// still recorded by the double, so filter on the window).
func reviewedPaths(reviewer *windowedAIReviewer) []string {
	var paths []string
	for _, request := range reviewer.requests {
		weight := 0
		for _, diff := range request.Diffs {
			weight += len(diff.Diff)
		}
		if weight > reviewer.windowBytes {
			continue
		}
		for _, diff := range request.Diffs {
			paths = append(paths, diff.Path)
		}
	}

	return paths
}

// overflowError builds the error shape a backend + retry decorator hand to
// the command layer when the prompt does not fit.
func overflowError(used, limit int) error {
	return fmt.Errorf(
		"%w (anthropic: prompt is too long: %d tokens > %d maximum)",
		support.ErrContextWindowExceeded, used, limit,
	)
}

// TestReviewInBatches pins the behaviour that replaces "this PR is too
// large, no review was posted": the change is split into batches that fit
// the model, reviewed one at a time, and merged back into a single review.
func TestReviewInBatches(t *testing.T) {
	t.Parallel()

	t.Run("should review every file exactly once across the batches", func(t *testing.T) {
		t.Parallel()

		// given: 8 files of 100 bytes, against a window that holds ~250
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(8, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(1000, 250), 20,
		)

		// then
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.ElementsMatch(
			t,
			[]string{"pkg/f0.go", "pkg/f1.go", "pkg/f2.go", "pkg/f3.go",
				"pkg/f4.go", "pkg/f5.go", "pkg/f6.go", "pkg/f7.go"},
			reviewedPaths(reviewer),
			"every changed file must reach the model exactly once — a batched review that silently drops files is worse than none",
		)
		assert.Len(t, result.Comments, 8,
			"the merged review must carry the findings from every batch")
		assert.NotContains(t, result.Summary, "could not be read",
			"a run that covered the whole PR must not claim files were skipped")
		assert.Contains(t, result.Summary, "8 of 8 changed files were reviewed")
	})

	t.Run("should keep shrinking the batch until it fits the window", func(t *testing.T) {
		t.Parallel()

		// given: a window far smaller than the naive halving would find, and
		// an overflow error with NO token figures, so the planner has to
		// discover the size by halving.
		reviewer := &windowedAIReviewer{windowBytes: 120}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(8, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request,
			errors.New("prompt is too long"), 20,
		)

		// then
		require.NoError(t, err)
		assert.Len(t, reviewedPaths(reviewer), 8,
			"the shrink ladder must converge on a batch size the model accepts, covering the whole PR")
		assert.Len(t, result.Comments, 8)
	})

	t.Run("should record a file larger than the window as unreviewed and still review the rest", func(t *testing.T) {
		t.Parallel()

		// given: one file that cannot fit at ANY split, plus files that can
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		diffs := fileDiffs(4, 100)
		diffs[1] = entities.FileDiff{Path: "vendor/huge.json", Diff: strings.Repeat("y", 5000)}
		request := entities.ReviewRequest{Diffs: diffs}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(9000, 250), 20,
		)

		// then
		require.NoError(t, err,
			"one unreviewable file must not sink the whole review — the other files still get read")
		assert.NotContains(t, reviewedPaths(reviewer), "vendor/huge.json")
		assert.Contains(t, result.Summary, "vendor/huge.json",
			"the author must be told exactly which file went unread")
		assert.Contains(t, result.Summary, "3 of 4 changed files were reviewed")
	})

	t.Run("should never approve a pull request it could not read in full", func(t *testing.T) {
		t.Parallel()

		// given: every batch approves, but one file is unreviewable —
		// approving would claim the whole change was examined.
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		diffs := fileDiffs(3, 100)
		diffs[0] = entities.FileDiff{Path: "vendor/huge.json", Diff: strings.Repeat("y", 5000)}
		request := entities.ReviewRequest{Diffs: diffs}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(9000, 250), 20,
		)

		// then
		require.NoError(t, err)
		assert.Equal(t, "comment", result.Verdict,
			"an approve verdict claims the whole diff was reviewed; with unread files it must degrade to comment")
	})

	t.Run("should keep the most severe verdict any batch reported", func(t *testing.T) {
		t.Parallel()

		// given: three batches whose verdicts disagree
		reviewer := &windowedAIReviewer{
			windowBytes: 250,
			verdicts:    []string{"approve", "request_changes", "comment"},
		}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(6, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(600, 250), 20,
		)

		// then
		require.NoError(t, err)
		assert.Equal(t, "request_changes", result.Verdict,
			"a blocking finding in ANY batch blocks the pull request — the merge must not average verdicts away")
	})

	t.Run("should keep the scripted verdicts pinned to batches, not to overflow probes", func(t *testing.T) {
		t.Parallel()

		// given: an overflow error with no token figures, so the planner has
		// to spend a probe before it finds a size that fits. The blocking
		// verdict is scripted for the FIRST reviewed batch — if the probe
		// consumed that slot, the run would come back `approve` and every
		// verdict-sequencing assertion in this file would be testing the
		// wrong batch.
		reviewer := &windowedAIReviewer{
			windowBytes: 250,
			verdicts:    []string{"request_changes"},
		}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(6, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, errors.New("prompt is too long"), 20,
		)

		// then
		require.Greater(t, len(reviewer.requests), reviewer.successfulCalls,
			"this scenario is only meaningful while the planner spends at least one overflow probe")
		assert.Equal(
			t,
			"request_changes",
			result.Verdict,
			"the first REVIEWED batch must receive the first scripted verdict, regardless of how many probes preceded it",
		)
		require.NoError(t, err)
	})

	t.Run("should carry every batch's summary into the merged one, bounded", func(t *testing.T) {
		t.Parallel()

		// given: each batch says something worth keeping, plus one runaway
		// summary — the merged text lands in the completion annotation, so it
		// must not bury the verdict under a wall of model prose.
		reviewer := &windowedAIReviewer{
			windowBytes: 250,
			summaries:   []string{"first batch findings", strings.Repeat("z", 4000), "third batch findings"},
		}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(6, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(600, 250), 20,
		)

		// then
		require.NoError(t, err)
		assert.Contains(t, result.Summary, "first batch findings",
			"a batch's own assessment must survive the merge")
		assert.Less(t, len(result.Summary), 2500,
			"the merged summary must stay bounded no matter how much prose the batches return")
	})

	t.Run("should survive a backend that returns neither a result nor an error", func(t *testing.T) {
		t.Parallel()

		// given: defensive — the repository contract does not forbid a
		// (nil, nil) return, and a batched run must not panic on one.
		reviewer := &nilResultAIReviewer{}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(4, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(400, 100), 20,
		)

		// then
		require.NoError(t, err)
		assert.Empty(t, result.Comments)
		assert.Equal(t, "comment", result.Verdict,
			"with no verdict from any batch the merge must fall back to the neutral one, never approve")
	})

	t.Run("should tell each batch which slice of the pull request it holds", func(t *testing.T) {
		t.Parallel()

		// given
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(6, 100), Attempt: 3}

		// when
		_, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(600, 250), 20,
		)

		// then
		require.NoError(t, err)
		require.NotEmpty(t, reviewer.requests)
		for i, issued := range reviewer.requests {
			assert.Equal(t, 6, issued.Batch.TotalFiles,
				"every batch must know the size of the whole pull request")
			assert.Positive(t, issued.Batch.Index, "every batch must be numbered")
			assert.True(t, issued.Batch.IsPartial(),
				"a batch carrying fewer files than the PR must be flagged partial so the prompt warns the model")
			assert.Zero(
				t,
				issued.Attempt,
				"the retry counter belongs to the retry decorator; batch %d must not inherit a stale attempt number",
				i+1,
			)
		}
	})

	t.Run("should stop at the batch cap and report the rest as unreviewed", func(t *testing.T) {
		t.Parallel()

		// given: 12 single-file batches but a cap of 2
		reviewer := &windowedAIReviewer{windowBytes: 150}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(12, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(1200, 150), 2,
		)

		// then
		require.NoError(t, err)
		assert.Contains(t, result.Summary, "could not be read",
			"a truncated run must never report full coverage")
		assert.Contains(t, result.Summary, "(+2 more)",
			"the unreviewed list must stay bounded but keep the true count")
		assert.Equal(t, "comment", result.Verdict,
			"a capped run leaves files unread, so it cannot approve")
	})

	t.Run("should return an error when not one batch could be reviewed", func(t *testing.T) {
		t.Parallel()

		// given: a backend that fails every call for a non-size reason
		reviewer := &windowedAIReviewer{
			windowBytes:      1 << 20,
			failEveryRequest: errors.New("backend exploded"),
		}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(4, 100)}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(400, 100), 20,
		)

		// then
		require.Error(
			t,
			err,
			"with nothing reviewed the caller must fall through to its failure annotation rather than post an empty review",
		)
		require.ErrorIs(t, err, support.ErrContextWindowExceeded,
			"the failure must still classify as a context-window overflow so the PR gets the too-large guidance")
		assert.Nil(t, result)
	})

	t.Run("should stop immediately when the context is cancelled", func(t *testing.T) {
		t.Parallel()

		// given: nobody is waiting for this review any more
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(8, 100)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		// when
		_, err := commands.ReviewInBatches(command, ctx, request, overflowError(800, 250), 20)

		// then
		require.Error(t, err)
		assert.Empty(t, reviewer.requests,
			"a cancelled review must not spend a single call on batches nobody will read")
	})
}

// TestReviewInBatchesConversation pins the re-review half of the fallback:
// each batch is shown only the prior threads that belong to its files, and
// the resolutions it returns are re-keyed to the whole-run numbering the
// post-pipeline matches against.
func TestReviewInBatchesConversation(t *testing.T) {
	t.Parallel()

	conversation := []entities.ReviewThread{
		{FilePath: "pkg/f0.go", Line: 1, ThreadID: 10},
		{FilePath: "pkg/f1.go", Line: 2, ThreadID: 11},
		{FilePath: "pkg/f2.go", Line: 3, ThreadID: 12},
		{FilePath: "pkg/f3.go", Line: 4, ThreadID: 13},
	}

	t.Run("should show each batch only the threads anchored to its own files", func(t *testing.T) {
		t.Parallel()

		// given: 4 files, a window that fits 2 at a time
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(4, 100), Conversation: conversation}

		// when
		_, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(400, 250), 20,
		)

		// then
		require.NoError(t, err)
		for _, issued := range reviewer.requests {
			batchPaths := make(map[string]struct{}, len(issued.Diffs))
			for _, diff := range issued.Diffs {
				batchPaths[diff.Path] = struct{}{}
			}
			for _, thread := range issued.Conversation {
				assert.Contains(t, batchPaths, thread.FilePath,
					"a batch must not be asked to judge a thread whose file it cannot see — it would resolve it blind")
			}
		}
	})

	t.Run("should re-key a batch's thread resolutions to the whole-run numbering", func(t *testing.T) {
		t.Parallel()

		// given: every batch answers about its own first thread as `T1`.
		// Without the re-key, batch 2's `T1` would resolve batch 1's thread.
		reviewer := &windowedAIReviewer{
			windowBytes: 250,
			resolutions: [][]entities.ThreadResolution{
				{{ID: "T1", Status: "resolved", Explanation: "first batch"}},
				{{ID: "T1", Status: "outstanding", Explanation: "second batch"}},
			},
		}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		request := entities.ReviewRequest{Diffs: fileDiffs(4, 100), Conversation: conversation}

		// when
		result, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(400, 250), 20,
		)

		// then
		require.NoError(t, err)
		require.Len(t, result.ThreadResolutions, 2)
		ids := []string{result.ThreadResolutions[0].ID, result.ThreadResolutions[1].ID}
		assert.Equal(
			t,
			[]string{"T1", "T3"},
			ids,
			"the second batch's local `T1` is the run's third thread; keeping it as `T1` would close a thread nobody addressed",
		)
	})

	t.Run("should hand threads whose file left the diff to the first batch", func(t *testing.T) {
		t.Parallel()

		// given: a prior thread on a file the latest iteration no longer
		// touches — no batch owns it, so it would never be classified and
		// would stay open on the PR forever.
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, nil, nil, nil)
		orphan := entities.ReviewThread{FilePath: "pkg/deleted.go", Line: 7, ThreadID: 99}
		request := entities.ReviewRequest{
			Diffs:        fileDiffs(4, 100),
			Conversation: append([]entities.ReviewThread{orphan}, conversation...),
		}

		// when
		_, err := commands.ReviewInBatches(
			command, context.Background(), request, overflowError(400, 250), 20,
		)

		// then
		require.NoError(t, err)
		require.NotEmpty(t, reviewer.requests)
		assert.Contains(t, reviewer.requests[0].Conversation, orphan,
			"a thread no batch owns must still be judged once, by the first batch")
	})
}

func TestInitialBatchBudget(t *testing.T) {
	t.Parallel()

	t.Run("should size the first batch from the reported token overage", func(t *testing.T) {
		t.Parallel()

		// given: 10 files of 1000 bytes; the backend says the prompt used
		// 4x the window, so roughly a quarter of the diff can fit.
		diffs := fileDiffs(10, 1000)
		total := commands.TotalDiffPromptWeight(diffs)

		// when
		budget := commands.InitialBatchBudget(diffs, overflowError(400000, 100000), 20)

		// then
		assert.Less(
			t,
			budget,
			total/2,
			"reading the overage must beat blind halving — halving a 4x-oversized prompt wastes two multi-megabyte round trips",
		)
		assert.Positive(t, budget)
	})

	t.Run("should halve the known-too-big size when the error carries no figures", func(t *testing.T) {
		t.Parallel()

		// given
		diffs := fileDiffs(10, 1000)
		total := commands.TotalDiffPromptWeight(diffs)

		// when
		budget := commands.InitialBatchBudget(diffs, errors.New("context window exceeded"), 20)

		// then
		assert.Equal(t, total/2, budget)
	})

	t.Run("should never return a budget that would re-send the failing prompt", func(t *testing.T) {
		t.Parallel()

		// given: figures that would scale to (nearly) the whole prompt
		diffs := fileDiffs(4, 100)
		total := commands.TotalDiffPromptWeight(diffs)

		// when
		budget := commands.InitialBatchBudget(diffs, overflowError(100001, 100000), 20)

		// then
		assert.Less(t, budget, total,
			"a budget at or above the total re-issues the prompt that just failed and the run makes no progress")
		assert.Positive(t, budget, "the budget must stay positive so takeBatch always yields a file")
	})

	t.Run("should not shrink below what the batch cap could ever cover", func(t *testing.T) {
		t.Parallel()

		// given: figures scraped out of prose can be wrong. A ratio that
		// implies 1000 batches must not collapse the budget — the cap would
		// allow only the first few, leaving most of the PR unread, and
		// nothing is gained by batches smaller than the cap can consume.
		diffs := fileDiffs(10, 1000)
		total := commands.TotalDiffPromptWeight(diffs)

		// when
		budget := commands.InitialBatchBudget(diffs, overflowError(100000000, 100000), 20)

		// then
		assert.GreaterOrEqual(t, budget, total/20,
			"the budget floor must keep a misread overage from destroying coverage")
	})
}

func TestMergeBatchVerdicts(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		current  string
		next     string
		expected string
	}{
		"first verdict wins over the empty zero value": {current: "", next: "approve", expected: "approve"},
		"request_changes outranks approve": {
			current:  "approve",
			next:     "request_changes",
			expected: "request_changes",
		},
		"approve never downgrades request_changes": {
			current:  "request_changes",
			next:     "approve",
			expected: "request_changes",
		},
		"comment outranks approve": {current: "approve", next: "comment", expected: "comment"},
		"request_changes outranks comment": {
			current:  "comment",
			next:     "request_changes",
			expected: "request_changes",
		},
		"an unrecognised verdict never overrides a known": {current: "approve", next: "banana", expected: "approve"},
	}
	for name, tc := range cases {
		t.Run("should keep the more severe verdict: "+name, func(t *testing.T) {
			t.Parallel()

			// given / when
			merged := commands.MergeBatchVerdicts(tc.current, tc.next)

			// then
			assert.Equal(t, tc.expected, merged)
		})
	}
}

func TestRemapBatchThreadResolutions(t *testing.T) {
	t.Parallel()

	t.Run("should rewrite batch-local ids to the run-global ones", func(t *testing.T) {
		t.Parallel()

		// given: this batch was shown the run's threads 2 and 4
		resolutions := []entities.ThreadResolution{
			{ID: "T1", Status: "resolved"},
			{ID: "T2", Status: "outstanding"},
		}

		// when
		remapped := commands.RemapBatchThreadResolutions(resolutions, []int{1, 3})

		// then
		require.Len(t, remapped, 2)
		assert.Equal(t, "T2", remapped[0].ID)
		assert.Equal(t, "T4", remapped[1].ID)
		assert.Equal(t, "resolved", remapped[0].Status, "only the id is rewritten; the verdict is untouched")
	})

	t.Run("should drop a resolution whose id names no thread in this batch", func(t *testing.T) {
		t.Parallel()

		// given: the model invented `T9` for a batch that held one thread.
		// Keeping it would let the (file, line) fallback misroute it.
		resolutions := []entities.ThreadResolution{{ID: "T9", Status: "resolved"}}

		// when
		remapped := commands.RemapBatchThreadResolutions(resolutions, []int{0})

		// then
		assert.Empty(t, remapped)
	})

	t.Run("should return nothing when the batch carried no threads", func(t *testing.T) {
		t.Parallel()

		// given / when
		remapped := commands.RemapBatchThreadResolutions(
			[]entities.ThreadResolution{{ID: "T1"}}, nil,
		)

		// then
		assert.Empty(t, remapped)
	})
}

func TestBuildBatchedReviewNoticeBody(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, 7, 23, 19, 42, 54, 0, time.UTC)

	t.Run("should explain the delay and quantify the change", func(t *testing.T) {
		t.Parallel()

		// given
		sizeCtx := commands.ReviewFailureContext{FileCount: 100, DiffBytes: 3879731}

		// when
		body := commands.BuildBatchedReviewNoticeBody(timestamp, sizeCtx)

		// then
		assert.Contains(t, body, "reviewing this PR in batches",
			"the author must learn the review is coming, not that it was skipped")
		assert.Contains(t, body, "100 files")
		assert.Contains(t, body, "3.7 MB")
		assert.Contains(t, body, "takes several times longer",
			"the whole point of the notice is to explain the wait")
		assert.Contains(t, body, "Batched review started at 2026-07-23T19:42:54Z.")
	})

	t.Run("should NOT set the review-once marker", func(t *testing.T) {
		t.Parallel()

		// given / when: the review has not finished, so a marker here would
		// make a webhook arriving mid-run conclude the PR was reviewed.
		body := commands.BuildBatchedReviewNoticeBody(timestamp, commands.ReviewFailureContext{})

		// then
		assert.False(t, support.HasCompletedReviewMarker([]string{body}),
			"the in-flight notice must not trip the review-once gate")
		assert.Contains(t, body, "**Code Guru ",
			"it must still carry the shared annotation prefix so the bot recognises its own comment")
	})

	t.Run("should omit the scale figures when the size is unknown", func(t *testing.T) {
		t.Parallel()

		// given / when
		body := commands.BuildBatchedReviewNoticeBody(timestamp, commands.ReviewFailureContext{})

		// then
		assert.Contains(t, body, "larger than the AI reviewer can read in a single pass")
		assert.NotContains(t, body, "**0 files**")
	})
}

func TestResolveMaxReviewBatches(t *testing.T) {
	t.Parallel()

	t.Run("should honour an explicit cap", func(t *testing.T) {
		t.Parallel()

		// given / when / then
		assert.Equal(t, 5, commands.ResolveMaxReviewBatches(5))
	})

	t.Run("should fall back to the shipped default when unset", func(t *testing.T) {
		t.Parallel()

		// given: a caller that never wired the budget (tests, hand-built
		// commands) must still get a BOUNDED run, not an unbounded one.
		expected := entities.AIConfig{}.ReviewBatches()

		// when / then
		assert.Equal(t, expected, commands.ResolveMaxReviewBatches(0))
		assert.Equal(t, expected, commands.ResolveMaxReviewBatches(-3))
	})
}

func TestDiffPromptWeight(t *testing.T) {
	t.Parallel()

	t.Run("should count the path and the fixed wrapper alongside the diff", func(t *testing.T) {
		t.Parallel()

		// given: a batch of many tiny files costs far more prompt than the
		// sum of its diffs — the header and fence dominate.
		diff := entities.FileDiff{Path: "a.go", Diff: "x"}

		// when
		weight := commands.DiffPromptWeight(diff)

		// then
		assert.Greater(t, weight, len(diff.Diff)+len(diff.Path),
			"ignoring the per-file wrapper lets a mass rename blow the budget on header text alone")
	})
}

// TestExecuteBatchesOversizedPullRequest drives the whole Execute path for
// the case this change targets: the first, whole-PR call overflows the
// window, and instead of the old "no review was posted" annotation the bot
// announces a batched run and posts a real review.
func TestExecuteBatchesOversizedPullRequest(t *testing.T) {
	t.Parallel()

	repo := forgeEntities.Repository{ID: "repo-1", Name: "demo"}
	pr := forgeEntities.PullRequestDetail{
		ID: 4242, Title: "big change", URL: "https://example/pr/4242",
	}

	newProvider := func(fileCount int) *recordingReviewProvider {
		files := make([]forgeEntities.PullRequestFile, 0, fileCount)
		for i := range fileCount {
			files = append(files, forgeEntities.PullRequestFile{
				Path:  fmt.Sprintf("pkg/f%d.go", i),
				Patch: strings.Repeat("x", 100),
			})
		}
		return &recordingReviewProvider{files: files}
	}

	t.Run("should announce the batched run and post the merged review", func(t *testing.T) {
		t.Parallel()

		// given: 6 files against a window that holds ~2 of them
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, &doubles.StubRulesRepository{}, nil, nil)
		provider := newProvider(6)

		// when
		result, err := command.Execute(
			context.Background(), provider, repo, pr,
			commands.ReviewOptions{BatchLargeReviews: true},
		)

		// then
		require.NoError(t, err)
		require.NotNil(t, result)
		assert.Len(t, result.Comments, 6, "every file's finding must survive the merge")

		bodies := make([]string, 0, len(provider.calls))
		for _, call := range provider.calls {
			bodies = append(bodies, call.body)
		}
		joined := strings.Join(bodies, "\n---\n")
		assert.Contains(t, joined, "reviewing this PR in batches",
			"the author must be told why the review is slow, instead of being told it was skipped")
		assert.NotContains(t, joined, "too large for the AI model's context window",
			"the give-up annotation must not fire when the batched review succeeded")
		assert.Contains(t, joined, "**Code Guru review complete",
			"a batched review still ends with the normal completion annotation")
	})

	t.Run("should not promise a batched run for a single oversized file", func(t *testing.T) {
		t.Parallel()

		// given: one file bigger than the window. There is nothing to split,
		// so announcing batches would promise a review the run cannot deliver.
		reviewer := &windowedAIReviewer{windowBytes: 50}
		command := commands.NewReviewCommand(reviewer, &doubles.StubRulesRepository{}, nil, nil)
		provider := newProvider(1)

		// when
		_, err := command.Execute(
			context.Background(), provider, repo, pr,
			commands.ReviewOptions{BatchLargeReviews: true},
		)

		// then
		require.Error(t, err)
		require.ErrorIs(t, err, support.ErrContextWindowExceeded)
		assert.Len(t, reviewer.requests, 1,
			"a one-file change must not be re-sent — the split would be identical to the prompt that just failed")

		var joined string
		var joinedSb869 strings.Builder
		for _, call := range provider.calls {
			joinedSb869.WriteString(call.body)
		}
		joined += joinedSb869.String()
		assert.NotContains(t, joined, "reviewing this PR in batches",
			"the bot must not promise a batched review it cannot perform")
		assert.Contains(t, joined, "too large for the AI model's context window")
	})

	t.Run("should fall back to the too-large annotation when batching is disabled", func(t *testing.T) {
		t.Parallel()

		// given: the operator opted out (`ai.batch_large_reviews: false`)
		reviewer := &windowedAIReviewer{windowBytes: 250}
		command := commands.NewReviewCommand(reviewer, &doubles.StubRulesRepository{}, nil, nil)
		provider := newProvider(6)

		// when
		_, err := command.Execute(
			context.Background(), provider, repo, pr,
			commands.ReviewOptions{BatchLargeReviews: false},
		)

		// then
		require.Error(t, err)
		require.ErrorIs(t, err, support.ErrContextWindowExceeded)
		assert.Len(t, reviewer.requests, 1,
			"with batching off the bot must not spend extra calls trying to split the change")

		var joined string
		var joinedSb898 strings.Builder
		for _, call := range provider.calls {
			joinedSb898.WriteString(call.body)
		}
		joined += joinedSb898.String()
		assert.Contains(t, joined, "too large for the AI model's context window",
			"the historical give-up annotation stays available for operators who opt out")
	})
}
