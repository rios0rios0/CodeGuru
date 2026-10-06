package support

import forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

// Verdict strings emitted by the AI backend response parser and the trivial
// PR detectors. Two distinct vocabularies share this surface: the LLM
// response parser (`internal/support/response_parser.go`) emits
// `approve` / `request_changes` / `comment`, while the trivial detectors
// (`internal/infrastructure/repositories/trivial/`) emit only
// `approve` / `reject` (no `comment` — trivial detection is a binary
// "let through or block" gate). The mapper below treats `reject` and
// `request_changes` as the same downstream verdict so both code paths
// reach SubmitPullRequestReview without an extra translation step.
const (
	verdictApprove        = "approve"
	verdictReject         = "reject"
	verdictRequestChanges = "request_changes"
	verdictComment        = "comment"
)

// MapVerdictToReview translates a code-guru verdict string and its summary
// body into a gitforge ReviewSubmission suitable for SubmitPullRequestReview.
//
// Cross-vocabulary mapping (see verdict constants for why both vocabularies
// reach this helper):
//
//	approve         -> ReviewVerdictApprove
//	reject          -> ReviewVerdictRequestChanges  (trivial detector vocab)
//	request_changes -> ReviewVerdictRequestChanges  (LLM parser vocab)
//	comment         -> ReviewVerdictWaitingForAuthor
//
// The `comment` verdict deliberately maps to "waiting for author" rather
// than to a native COMMENT review: on Azure DevOps that lands as vote `-5`
// (the documented "waiting on the author" signal), and on GitHub gitforge
// translates `WaitingForAuthor` to `event=COMMENT` so the review surfaces
// without flipping the PR into a "Changes requested" hard block. This
// keeps the bot's "I have something to flag but no strong opinion" review
// visible in the platform's reviewer panel on every AI run.
//
// The ok return is false only for verdicts the parser does not recognise
// — a corrupt LLM payload should not cause a spurious vote.
func MapVerdictToReview(verdict, summary string) (forgeEntities.ReviewSubmission, bool) {
	switch verdict {
	case verdictApprove:
		return forgeEntities.ReviewSubmission{
			Verdict: forgeEntities.ReviewVerdictApprove,
			Body:    summary,
		}, true
	case verdictReject, verdictRequestChanges:
		return forgeEntities.ReviewSubmission{
			Verdict: forgeEntities.ReviewVerdictRequestChanges,
			Body:    summary,
		}, true
	case verdictComment:
		return forgeEntities.ReviewSubmission{
			Verdict: forgeEntities.ReviewVerdictWaitingForAuthor,
			Body:    summary,
		}, true
	}
	return forgeEntities.ReviewSubmission{}, false
}
