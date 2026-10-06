package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/domain/repositories"
	"github.com/rios0rios0/codeguru/internal/support"
)

// perFileBatchOverheadBytes approximates the prompt bytes a single file
// costs on top of its own diff — the `### File: <path> (<lang>)` header and
// the surrounding ```diff fence. Counting it keeps a batch of many tiny
// files (a mass rename, a lockfile refresh) from blowing the budget on
// wrapper text alone.
const perFileBatchOverheadBytes = 48

// batchBudgetSafetyFactor shrinks the per-batch diff budget derived from a
// provider's reported token overage. The ratio `limit / used` describes the
// WHOLE prompt, but only the diff part shrinks between batches — the rules,
// the project guidelines, and the conversation are re-sent verbatim every
// time. Scaling below the naive ratio absorbs that fixed overhead, so the
// first batch usually fits instead of costing another multi-megabyte round
// trip to discover it does not.
const batchBudgetSafetyFactor = 0.8

// maxBatchBudgetShrinks bounds the extra calls the reviewer may spend
// discovering a batch size that fits, on top of the operator's batch cap.
// Each shrink halves the budget, so this covers a pull request roughly
// 4000x the model's window before the run is cut short — far past any real
// diff, while still guaranteeing the loop terminates if a backend reports
// "prompt too long" for a reason that has nothing to do with size.
const maxBatchBudgetShrinks = 12

// maxBatchedSummaryBytes bounds the concatenated per-batch summaries in the
// merged review summary. The summary is rendered into the PR-wide
// completion annotation, so an unbounded join across 20 batches would bury
// the verdict under a wall of prose.
const maxBatchedSummaryBytes = 1500

// maxUnreviewedPathsShown bounds how many unreviewed file paths the merged
// summary names before collapsing the rest into a `(+N more)` sentinel,
// mirroring `summarizeStaleFilePaths`.
const maxUnreviewedPathsShown = 8

// minBatchableFiles is the smallest change batching can help with. One
// file cannot be split across batches, so an overflow on a single-file
// pull request is genuinely unreviewable and belongs on the "too large"
// annotation path rather than in a batched run that cannot progress.
const minBatchableFiles = 2

// batchBudgetHalvingDivisor is the factor the batch budget is divided by
// when the reviewer has to search for a size that fits: halving is the
// classic bisection step, and each step costs exactly one wasted call
// before the search narrows.
const batchBudgetHalvingDivisor = 2

// resolveMaxReviewBatches resolves the per-review batch cap. Zero (the
// "unset" state used by hand-built commands and tests) falls back to the
// shipped default from `entities.AIConfig.ReviewBatches()`, so a caller
// that never wires the budget still gets a bounded run rather than an
// unbounded one — the same contract `MaxGuidelinesBytes` follows.
func resolveMaxReviewBatches(configured int) int {
	if configured > 0 {
		return configured
	}

	return entities.AIConfig{}.ReviewBatches()
}

// Verdict severity ranks used when merging the per-batch verdicts. An
// unknown verdict ranks `verdictRankUnknown` and therefore never overrides
// a recognised one; `batchReviewer.mergedVerdict` supplies the canonical
// fallback when every batch returned something unrecognised.
const (
	verdictRankUnknown  = 0
	verdictRankApprove  = 1
	verdictRankComment  = 2
	verdictRankBlocking = 3
)

// batchVerdictSeverityRank orders the review verdicts by severity so
// merging the per-batch verdicts is a lookup rather than a chain of
// comparisons: the most severe verdict any batch reported is the verdict
// for the pull request as a whole.
//
// `reject` is ranked defensively: it belongs to the trivial-detector
// vocabulary rather than the LLM's, and batching never runs on the trivial
// path, but ranking it with `request_changes` means a backend that emits it
// can never be read as "less severe than approve".
func batchVerdictSeverityRank(verdict string) int {
	return map[string]int{
		verdictApprove:        verdictRankApprove,
		verdictComment:        verdictRankComment,
		verdictRequestChanges: verdictRankBlocking,
		verdictReject:         verdictRankBlocking,
	}[verdict]
}

// reviewLargePullRequest is the context-window fallback: it tells the pull
// request what is happening and then reviews the change in batches.
//
// The notice is posted FIRST and deliberately: a batched run makes several
// sequential model calls, so the author waits several times longer than
// usual after the "reviewing" marker. Without the notice that delay is
// indistinguishable from the bot having crashed — the exact failure mode
// the "reviewing" marker was introduced to remove — and an author who
// assumes the bot is dead merges before the review lands.
//
// The notice carries the shared `**Code Guru ` annotation prefix (so the
// bot still recognises its own comments) but deliberately NOT the
// `**Code Guru review` completed/failed marker: the review has not
// finished, and setting the review-once gate here would make a webhook
// arriving mid-run conclude the pull request was already reviewed.
func (c *ReviewCommand) reviewLargePullRequest(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	request entities.ReviewRequest,
	overflowErr error,
	sizeCtx reviewFailureContext,
	opts ReviewOptions,
) (*entities.ReviewResult, error) {
	// A one-file change has nothing to split: batching would re-send the
	// same prompt, fail identically, and leave the pull request with a
	// "reviewing in batches" promise the run cannot keep. Hand the overflow
	// straight back so the caller posts the honest "too large" annotation.
	if len(request.Diffs) < minBatchableFiles {
		logger.Warnf(
			"PR #%d: a single file exceeds the model context window; batching cannot split it",
			prID,
		)

		return nil, overflowErr
	}

	logger.Warnf(
		"PR #%d: prompt exceeds the model context window (%d files, %s of diff); reviewing in batches",
		prID, sizeCtx.FileCount, humanizeBytes(sizeCtx.DiffBytes),
	)
	if !opts.DryRun {
		c.postBatchedReviewNotice(ctx, provider, repo, prID, sizeCtx)
	}

	return c.reviewInBatches(ctx, request, overflowErr, resolveMaxReviewBatches(opts.MaxReviewBatches))
}

// postBatchedReviewNotice drops the single PR-wide notice that replaces the
// old "this PR is too large, no review was posted" annotation. Best-effort
// on the same terms as every other annotation: a failure logs at `Warn` and
// the batched review still runs — the notice is UX, not a correctness gate.
func (c *ReviewCommand) postBatchedReviewNotice(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	sizeCtx reviewFailureContext,
) {
	body := buildBatchedReviewNoticeBody(time.Now().UTC(), sizeCtx)
	logger.Infof("PR #%d: posting 'reviewing in batches' notice", prID)

	timeoutCtx, cancel := context.WithTimeout(ctx, reviewingMarkerPostTimeout)
	defer cancel()
	if err := provider.PostPullRequestComment(
		timeoutCtx,
		repo,
		prID,
		body,
		forgeEntities.WithThreadStatus(annotationThreadStatus),
	); err != nil {
		logger.Warnf("PR #%d: failed to post 'reviewing in batches' notice: %v", prID, err)
	}
}

// buildBatchedReviewNoticeBody renders that notice. Pure function — exposed
// via `export_test.go` so the formatting contract is unit-testable without
// a stub provider, and forcing UTC inside the helper like its siblings.
//
// It replaces the old too-large annotation's "no review was posted, split
// the PR" message with the same scale figures and the same split-it-up
// advice, reframed around what now actually happens: the review IS coming,
// it is simply slower. The advice stays because a batched review is a
// worse review — each batch sees only its own slice, so cross-file findings
// that span batches are invisible to it.
func buildBatchedReviewNoticeBody(now time.Time, sizeCtx reviewFailureContext) string {
	lead := sizeCtx.leadSentence(
		"This pull request is larger than the AI reviewer can read in a single pass",
	)

	return fmt.Sprintf(
		"\xe2\x8f\xb3 **Code Guru is reviewing this PR in batches.**\n\n"+
			"%s, so the change is being split into smaller batches that are reviewed one after another. "+
			"The review will still be posted — it just takes several times longer than usual. "+
			"Please wait for the \"review complete\" notice before merging.\n\n"+
			"Each batch only sees its own files, so a finding that spans two batches can be missed. "+
			"For a faster and better review:\n"+
			"- **Split the change into several smaller, focused pull requests.**\n"+
			"- **Exclude generated, vendored, or lock files** (for example `*.lock`, build output, "+
			"`dist/`, snapshots) — they inflate the diff without needing review.\n\n"+
			"_Batched review started at %s._",
		lead,
		now.UTC().Format(time.RFC3339),
	)
}

// reviewInBatches reviews a pull request whose assembled prompt does not
// fit the AI model's context window, by splitting its files into batches
// that DO fit and merging the per-batch reviews into a single result.
//
// This is the fallback for `support.ErrContextWindowExceeded`. The
// alternative — the behaviour this replaces — was to post a "your PR is too
// large, split it up" notice and review nothing at all, which left exactly
// the pull requests that most need a reviewer with none.
//
// The batch size is discovered, not configured, because the usable size
// depends on the model, the beta flags in play, and how much of the window
// the rules and project guidelines already consume. The first budget comes
// from the token figures in the overflow error when the backend reports
// them (`support.ParseContextWindowOverage`), and halves on any batch that
// still overflows. A single file that overflows on its own cannot be
// reviewed at any budget and is recorded as unreviewed instead.
//
// Returns an error only when NOT ONE batch produced a review — the caller
// then posts its usual failure annotation. A partially successful run
// returns the merged review plus a summary naming what could not be read,
// because a review of 90% of a pull request is worth far more to the author
// than no review at all.
func (c *ReviewCommand) reviewInBatches(
	ctx context.Context,
	request entities.ReviewRequest,
	overflowErr error,
	maxBatches int,
) (*entities.ReviewResult, error) {
	reviewer := newBatchReviewer(c.aiReviewer, request, overflowErr, maxBatches)
	if err := reviewer.run(ctx, maxBatches); err != nil {
		return nil, err
	}

	return reviewer.result(), nil
}

// batchReviewer owns one batched review run: the queue of files still to
// review, the current per-batch byte budget, and the merged output. It is
// single-use and not safe for concurrent use — batches run sequentially on
// purpose, so a pull request that overflows the window cannot also
// multiply the load the bot puts on the backend.
type batchReviewer struct {
	reviewer repositories.AIReviewerRepository
	// base is the whole-pull-request request every batch is derived from;
	// its Diffs field is never sent as-is.
	base entities.ReviewRequest

	// pending holds the files not yet consumed by a batch, in the order
	// the provider listed them, so related files usually land together.
	pending []entities.FileDiff
	// budget is the maximum prompt weight (see diffPromptWeight) a single
	// batch may carry. Halves on every batch that still overflows.
	budget int
	// changedPaths is the normalised set of every path in the pull
	// request, used to route prior review threads whose file is no longer
	// part of the change.
	changedPaths map[string]struct{}
	totalFiles   int

	// calls counts every backend invocation, including the ones spent
	// discovering a batch size that fits.
	calls int
	// completed / failures count batches that returned a review and
	// batches that returned an error, respectively.
	completed int
	failures  int

	comments    []entities.ReviewComment
	resolutions []entities.ThreadResolution
	summaries   []string
	verdict     string
	// unreviewed names every file that never reached the model, whatever
	// the reason (a single file larger than the window, a failed batch,
	// or the batch cap cutting the run short).
	unreviewed []string
}

// newBatchReviewer seeds a run from the request that overflowed and the
// error that reported the overflow.
func newBatchReviewer(
	reviewer repositories.AIReviewerRepository,
	base entities.ReviewRequest,
	overflowErr error,
	maxBatches int,
) *batchReviewer {
	changedPaths := make(map[string]struct{}, len(base.Diffs))
	for _, diff := range base.Diffs {
		changedPaths[normalizeFilePath(diff.Path)] = struct{}{}
	}

	return &batchReviewer{
		reviewer:     reviewer,
		base:         base,
		pending:      base.Diffs,
		budget:       initialBatchBudget(base.Diffs, overflowErr, maxBatches),
		changedPaths: changedPaths,
		totalFiles:   len(base.Diffs),
	}
}

// initialBatchBudget sizes the first batch. When the backend reported how
// many tokens the prompt used against how many the model allows, the same
// proportion is applied to the diff bytes (scaled by
// batchBudgetSafetyFactor for the non-diff prompt overhead that does not
// shrink between batches). Without usable figures it falls back to halving
// the size that is already known to be too big.
//
// The result is clamped into `[total/maxBatches, total/2]`:
//
//   - the ceiling keeps the run from re-sending the prompt that just
//     failed, so it always makes progress;
//   - the floor keeps a misread error from destroying coverage. The token
//     figures are scraped from free-form provider prose, and a message that
//     happens to carry two unrelated token counts would otherwise scale the
//     budget to near-nothing — producing hundreds of tiny batches, of which
//     the cap allows only the first few, leaving most of the pull request
//     unread. Below `total/maxBatches` the cap truncates the run anyway, so
//     nothing is gained by going smaller; the shrink ladder still finds the
//     real size if the floor turns out not to fit.
func initialBatchBudget(diffs []entities.FileDiff, overflowErr error, maxBatches int) int {
	total := totalDiffPromptWeight(diffs)
	budget := total / batchBudgetHalvingDivisor

	if overflowErr != nil {
		if used, limit, ok := support.ParseContextWindowOverage(overflowErr.Error()); ok {
			scaled := float64(total) * float64(limit) / float64(used) * batchBudgetSafetyFactor
			budget = int(scaled)
			logger.Debugf(
				"batched review: backend reported %d tokens used against a %d limit; first batch budget is %d bytes of diff",
				used,
				limit,
				budget,
			)
		}
	}

	if budget >= total {
		budget = total / batchBudgetHalvingDivisor
	}
	if maxBatches > 0 {
		budget = max(budget, total/maxBatches)
	}

	return max(budget, 1)
}

// run drives the batches until every file has been consumed or a cap
// stops the run. It returns an error only when no batch produced a review;
// a partial run is a success with the gaps recorded on the receiver.
func (b *batchReviewer) run(ctx context.Context, maxBatches int) error {
	maxCalls := maxBatches + maxBatchBudgetShrinks
	for len(b.pending) > 0 && b.completed+b.failures < maxBatches && b.calls < maxCalls {
		// A cancelled context means nobody is waiting for this review any
		// more (PR closed mid-flight, pod draining). Checking BEFORE each
		// batch matters more here than anywhere else in the pipeline: a
		// batched run is the longest-lived work the bot does, so it is the
		// most likely to still be spending model calls on a pull request
		// that was merged or abandoned ten minutes ago.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("batched AI review cancelled after %d batch(es): %w", b.completed, ctxErr)
		}

		batch := b.takeBatch()
		b.calls++

		request, threadIndexes := b.requestFor(batch)
		result, err := b.reviewer.ReviewDiff(ctx, request)
		if err == nil {
			b.absorb(result, batch, threadIndexes)
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("batched AI review cancelled after %d batch(es): %w", b.completed, ctxErr)
		}
		if errors.Is(err, support.ErrContextWindowExceeded) && b.shrink(batch) {
			continue
		}
		b.recordFailure(batch, err)
	}
	b.recordRemainingUnreviewed()

	if b.completed == 0 {
		return fmt.Errorf(
			"batched AI review produced no reviewable batch after %d call(s) across %d file(s): %w",
			b.calls, b.totalFiles, support.ErrContextWindowExceeded,
		)
	}
	logger.Infof(
		"batched review finished: %d batch(es) reviewed %d/%d files (%d failed batch(es), %d call(s))",
		b.completed, b.totalFiles-len(b.unreviewed), b.totalFiles, b.failures, b.calls,
	)

	return nil
}

// takeBatch returns the longest prefix of the pending files that fits the
// current budget. The files are NOT consumed here: an overflowing batch is
// re-taken at a smaller budget, and only a batch that has been dealt with
// (reviewed, failed, or skipped) is dropped from the queue.
//
// A single file heavier than the whole budget is still returned on its own
// — refusing to emit it would stall the queue, and a one-file batch is
// exactly the signal `shrink` needs to stop halving.
func (b *batchReviewer) takeBatch() []entities.FileDiff {
	weight := 0
	for i, diff := range b.pending {
		fileWeight := diffPromptWeight(diff)
		if i > 0 && weight+fileWeight > b.budget {
			return b.pending[:i]
		}
		weight += fileWeight
	}

	return b.pending
}

// shrink halves the budget after a batch overflowed, so the same files are
// re-taken in smaller slices. It reports false when the batch is already a
// single file — that file cannot be made to fit by any split, so the caller
// records it as unreviewed and moves on rather than looping forever.
func (b *batchReviewer) shrink(batch []entities.FileDiff) bool {
	if len(batch) <= 1 {
		return false
	}
	b.budget = max(totalDiffPromptWeight(batch)/batchBudgetHalvingDivisor, 1)
	logger.Debugf(
		"batched review: %d-file batch still exceeds the context window; retrying at %d bytes of diff per batch",
		len(batch), b.budget,
	)

	return true
}

// requestFor derives one batch's review request from the whole-PR request:
// the batch's own files, the batch framing the prompt renders so the model
// knows it is looking at a slice, and the slice of the prior conversation
// that belongs to those files. The second return value carries the
// conversation index mapping the caller needs to fold the batch's thread
// resolutions back into the run (see remapBatchThreadResolutions).
//
// `Attempt` is reset because retries are the decorator's business, and a
// stale attempt number would make the prompt claim a JSON parse failure
// that never happened on this batch.
func (b *batchReviewer) requestFor(batch []entities.FileDiff) (entities.ReviewRequest, []int) {
	threads, threadIndexes := b.conversationFor(batch)

	request := b.base
	request.Diffs = batch
	request.Attempt = 0
	request.Batch = entities.ReviewBatch{
		Index:      b.completed + b.failures + 1,
		Files:      len(batch),
		TotalFiles: b.totalFiles,
	}
	request.Conversation = threads

	return request, threadIndexes
}

// conversationFor selects the prior review threads that belong to a batch:
// every thread anchored to one of the batch's files, plus — on the FIRST
// batch only — the threads whose file is no longer part of the change at
// all, which would otherwise never be classified and would stay open on
// the pull request forever.
//
// The second return value maps each selected thread back to its index in
// the whole-PR conversation. The prompt numbers threads by their position
// in the slice it is given (`support.ThreadPromptID`), so a batch's `T1` is
// not the run's `T1`; the post-pipeline matches resolutions against the
// whole conversation, and without this mapping every batch's resolutions
// would be applied to the wrong threads.
func (b *batchReviewer) conversationFor(batch []entities.FileDiff) ([]entities.ReviewThread, []int) {
	if len(b.base.Conversation) == 0 {
		return nil, nil
	}

	batchPaths := make(map[string]struct{}, len(batch))
	for _, diff := range batch {
		batchPaths[normalizeFilePath(diff.Path)] = struct{}{}
	}
	isFirstBatch := b.completed+b.failures == 0

	var threads []entities.ReviewThread
	var indexes []int
	for index, thread := range b.base.Conversation {
		path := normalizeFilePath(thread.FilePath)
		_, inBatch := batchPaths[path]
		_, stillChanged := b.changedPaths[path]
		if inBatch || (isFirstBatch && !stillChanged) {
			threads = append(threads, thread)
			indexes = append(indexes, index)
		}
	}

	return threads, indexes
}

// absorb folds one successful batch into the merged review and drops its
// files from the queue. `threadIndexes` is the mapping `requestFor`
// produced for this same batch.
func (b *batchReviewer) absorb(
	result *entities.ReviewResult,
	batch []entities.FileDiff,
	threadIndexes []int,
) {
	b.completed++
	b.pending = b.pending[len(batch):]

	if result == nil {
		return
	}
	b.comments = append(b.comments, result.Comments...)
	b.verdict = mergeBatchVerdicts(b.verdict, result.Verdict)
	if summary := strings.TrimSpace(result.Summary); summary != "" {
		b.summaries = append(b.summaries, summary)
	}
	b.resolutions = append(
		b.resolutions,
		remapBatchThreadResolutions(result.ThreadResolutions, threadIndexes)...,
	)
}

// recordFailure drops a batch that could not be reviewed — either the
// backend errored after its own retries, or the batch is a single file
// larger than the context window. The files are named in the merged
// summary so the author knows exactly which parts of the change went
// unread rather than assuming the whole diff was covered.
func (b *batchReviewer) recordFailure(batch []entities.FileDiff, cause error) {
	b.failures++
	b.pending = b.pending[len(batch):]
	for _, diff := range batch {
		b.unreviewed = append(b.unreviewed, diff.Path)
	}
	logger.Warnf(
		"batched review: batch of %d file(s) failed and was skipped: %v",
		len(batch), cause,
	)
}

// recordRemainingUnreviewed books whatever is still queued when a cap ends
// the run, so a truncated review never silently claims full coverage.
func (b *batchReviewer) recordRemainingUnreviewed() {
	for _, diff := range b.pending {
		b.unreviewed = append(b.unreviewed, diff.Path)
	}
	if len(b.pending) > 0 {
		logger.Warnf(
			"batched review: stopped with %d file(s) unreviewed after %d batch(es) — "+
				"raise ai.max_review_batches or split the pull request",
			len(b.pending), b.completed+b.failures,
		)
	}
	b.pending = nil
}

// result assembles the merged review handed back to the normal post
// pipeline: the union of every batch's comments and thread resolutions,
// the most severe verdict any batch reported, and a summary that states
// the review was batched and what it could not read.
func (b *batchReviewer) result() *entities.ReviewResult {
	return &entities.ReviewResult{
		Verdict:           b.mergedVerdict(),
		Comments:          b.comments,
		ThreadResolutions: b.resolutions,
		Summary:           b.buildSummary(),
	}
}

// mergedVerdict resolves the run's verdict. A pull request the bot could
// not read in full is never approved: an approval claims the whole change
// was examined, and downgrading to `comment` keeps the reviewer panel
// honest about the gap instead of green-lighting unread files.
func (b *batchReviewer) mergedVerdict() string {
	verdict := b.verdict
	if batchVerdictSeverityRank(verdict) == verdictRankUnknown {
		verdict = verdictComment
	}
	if verdict == verdictApprove && len(b.unreviewed) > 0 {
		return verdictComment
	}

	return verdict
}

// buildSummary renders the merged summary shown in the completion
// annotation: how the review was split, how much of the change it covered,
// and the per-batch summaries the model produced.
func (b *batchReviewer) buildSummary() string {
	reviewed := b.totalFiles - len(b.unreviewed)

	var summary strings.Builder
	fmt.Fprintf(
		&summary,
		"Reviewed in %d batches because this pull request is larger than the AI model's context window. ",
		b.completed+b.failures,
	)
	fmt.Fprintf(
		&summary,
		"%d of %d changed %s reviewed",
		reviewed, b.totalFiles, pluralizeFilesWere(reviewed),
	)
	if len(b.unreviewed) > 0 {
		fmt.Fprintf(
			&summary,
			"; %d could not be read (%s)",
			len(b.unreviewed), summarizeUnreviewedPaths(b.unreviewed),
		)
	}
	summary.WriteString(".")

	if joined := strings.TrimSpace(strings.Join(b.summaries, " ")); joined != "" {
		summary.WriteString("\n\n")
		summary.WriteString(support.Truncate(joined, maxBatchedSummaryBytes))
	}

	return summary.String()
}

// mergeBatchVerdicts keeps the more severe of two verdicts, ranked by
// batchVerdictSeverityRank. An empty or unrecognised incoming verdict never
// downgrades what earlier batches already reported.
func mergeBatchVerdicts(current, next string) string {
	if batchVerdictSeverityRank(next) > batchVerdictSeverityRank(current) {
		return next
	}

	return current
}

// remapBatchThreadResolutions rewrites each resolution's synthetic thread
// id from the batch-local numbering the model was shown
// (`T1`, `T2`, ... over the batch's conversation slice) to the whole-run
// numbering the post-pipeline matches against.
//
// Without the rewrite, batch 2's `T1` would resolve batch 1's first thread
// — the bot would reply "fixed" on a thread nobody addressed and auto-close
// it. Entries whose id does not name a thread in this batch are dropped:
// the id cannot be trusted to mean anything, and the (file, line) fallback
// in `matchResolutionThread` would just as happily misroute it.
func remapBatchThreadResolutions(
	resolutions []entities.ThreadResolution,
	threadIndexes []int,
) []entities.ThreadResolution {
	if len(resolutions) == 0 || len(threadIndexes) == 0 {
		return nil
	}

	globalID := make(map[string]string, len(threadIndexes))
	for local, global := range threadIndexes {
		globalID[support.ThreadPromptID(local)] = support.ThreadPromptID(global)
	}

	remapped := make([]entities.ThreadResolution, 0, len(resolutions))
	for _, resolution := range resolutions {
		id, ok := globalID[resolution.ID]
		if !ok {
			logger.Debugf(
				"batched review: dropping thread resolution with unknown batch-local id %q on %s:%d",
				resolution.ID, resolution.FilePath, resolution.Line,
			)
			continue
		}
		resolution.ID = id
		remapped = append(remapped, resolution)
	}

	return remapped
}

// diffPromptWeight estimates how many prompt bytes one file contributes:
// its diff, its path (rendered in the file header), and the fixed wrapper
// around both.
func diffPromptWeight(diff entities.FileDiff) int {
	return len(diff.Diff) + len(diff.Path) + perFileBatchOverheadBytes
}

// totalDiffPromptWeight sums diffPromptWeight across a file set.
func totalDiffPromptWeight(diffs []entities.FileDiff) int {
	total := 0
	for _, diff := range diffs {
		total += diffPromptWeight(diff)
	}

	return total
}

// summarizeUnreviewedPaths joins up to maxUnreviewedPathsShown paths and
// collapses the rest into a `(+N more)` sentinel, so a run that skipped a
// large batch does not bury the summary under a file listing.
func summarizeUnreviewedPaths(paths []string) string {
	if len(paths) <= maxUnreviewedPathsShown {
		return strings.Join(paths, ", ")
	}

	return fmt.Sprintf(
		"%s (+%d more)",
		strings.Join(paths[:maxUnreviewedPathsShown], ", "),
		len(paths)-maxUnreviewedPathsShown,
	)
}

// pluralizeFilesWere returns the noun+verb pair matching a file count, so
// the summary reads "1 of 40 changed file was reviewed" rather than
// "1 files were".
func pluralizeFilesWere(n int) string {
	if n == 1 {
		return "file was"
	}

	return "files were"
}
