package support

import (
	"sort"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// BuildReviewConversation assembles the bot's inline review threads on
// a PR into a chronologically-ordered list of ReviewThreads, each one
// rooted on a previous bot comment and carrying every reply (user
// follow-up, bot self-reply, anyone else) in dialogue order.
//
// The walk is deliberately rooted on bot-authored top-level comments
// only: those are the threads the LLM should re-read on a re-review,
// because they are the conversations the bot started. User-only
// threads (reviewers asking each other questions, off-topic chat) are
// not relevant input to the AI and would dilute the prompt.
//
// Identifying "the bot's" comments is provider-specific (GitHub uses
// `<app-name>[bot]`, Azure DevOps uses the bot's account uniqueName);
// rather than hard-coding a list, the caller passes a predicate so the
// matcher stays out of this package and the assembler stays pure.
//
// `liveFiles` is the set of file paths in the current PR diff. Threads
// anchored to a file outside that set are dropped — without the filter,
// a re-review would see prior bot comments on lines that are no longer
// in the diff and try to "respond" inline on a stale anchor (the
// post-pipeline's `dropStaleComments` would then drop them, but only
// after the LLM has already wasted tokens deliberating). Passing nil
// keeps every thread regardless of file (used by tests that have no
// diff to compare against).
//
// Algorithm:
//
//  1. Collect every comment whose author satisfies isBot AND that is a
//     top-level inline comment (Line > 0, InReplyToID == 0) AND that
//     is anchored to a file present in liveFiles. Each becomes a
//     thread root.
//  2. For every other inline comment whose InReplyToID points at one of
//     those roots (directly or transitively via a chain of replies),
//     append it to that thread's Comments.
//  3. Sort each thread's reply chain by comment ID (stable proxy for
//     creation order — every provider returns IDs that monotonically
//     increase by creation time).
//  4. Sort the resulting threads by FilePath + Line so the prompt's
//     conversation block reads top-to-bottom alongside the diff.
//
// PR-wide comments (Line == 0) are skipped: they are the bot's
// markers / completion annotations / failure notices, not review
// findings the LLM should re-engage with.
func BuildReviewConversation(
	comments []forgeEntities.PullRequestComment,
	isBot func(author string) bool,
	liveFiles map[string]struct{},
) []entities.ReviewThread {
	if len(comments) == 0 || isBot == nil {
		return nil
	}

	// Index every comment by ID so the reply walk can resolve parent
	// chains in O(1) per comment.
	byID := make(map[int64]forgeEntities.PullRequestComment, len(comments))
	for _, c := range comments {
		byID[c.ID] = c
	}

	roots := discoverThreadRoots(comments, isBot, liveFiles)
	if len(roots) == 0 {
		return nil
	}

	// Append every reply that resolves to one of the bot's thread
	// roots. Replies can chain (reply-to-a-reply), so walk the
	// InReplyToID pointer up to the root.
	type orderedReply struct {
		rootID int64
		id     int64
		msg    entities.ReviewMessage
	}
	var replies []orderedReply
	for _, c := range comments {
		if c.Line <= 0 || c.InReplyToID == 0 {
			continue
		}
		rootID := walkToRoot(c, byID, roots)
		if rootID == 0 {
			continue
		}
		replies = append(replies, orderedReply{
			rootID: rootID,
			id:     c.ID,
			msg:    entities.ReviewMessage{Author: c.Author, Body: c.Body},
		})
	}

	// Sort replies by ID so the prompt reads chronologically (every
	// provider issues IDs in creation order — gitforge's int64
	// normalisation preserves that invariant).
	sort.Slice(replies, func(i, j int) bool {
		return replies[i].id < replies[j].id
	})
	for _, reply := range replies {
		root := roots[reply.rootID]
		root.Comments = append(root.Comments, reply.msg)
	}

	// Materialise + sort threads by file then line for a stable,
	// readable prompt block.
	threads := make([]entities.ReviewThread, 0, len(roots))
	for _, t := range roots {
		threads = append(threads, *t)
	}
	sort.Slice(threads, func(i, j int) bool {
		if threads[i].FilePath != threads[j].FilePath {
			return threads[i].FilePath < threads[j].FilePath
		}
		return threads[i].Line < threads[j].Line
	})
	return threads
}

// discoverThreadRoots collects every top-level inline comment authored
// by the bot — these become the conversation's thread roots. Comments
// anchored to a file outside `liveFiles` are dropped (when liveFiles is
// non-nil) so a re-review's prompt does not include stale anchors. The
// returned map is keyed by the root comment's ID so the reply walk can
// match `InReplyToID` chains in O(1).
func discoverThreadRoots(
	comments []forgeEntities.PullRequestComment,
	isBot func(author string) bool,
	liveFiles map[string]struct{},
) map[int64]*entities.ReviewThread {
	roots := map[int64]*entities.ReviewThread{}
	for _, c := range comments {
		if c.Line <= 0 || c.InReplyToID != 0 || !isBot(c.Author) {
			continue
		}
		if liveFiles != nil {
			if _, live := liveFiles[normalizeConversationFilePath(c.FilePath)]; !live {
				continue
			}
		}
		roots[c.ID] = &entities.ReviewThread{
			FilePath:      c.FilePath,
			Line:          c.Line,
			ThreadID:      c.ThreadID,
			RootCommentID: c.ID,
			Comments: []entities.ReviewMessage{
				{Author: c.Author, Body: c.Body},
			},
		}
	}
	return roots
}

// normalizeConversationFilePath strips a leading `/` so ADO-shape
// paths (`/internal/foo.go`) compare equal to the AI-emitted
// `internal/foo.go` form. Mirrors the same normalisation the dedup
// pipeline uses on the post side.
func normalizeConversationFilePath(p string) string {
	if len(p) > 0 && p[0] == '/' {
		return p[1:]
	}
	return p
}

// walkToRoot resolves a reply's chain of InReplyToID pointers up to a
// known thread root. Returns the matching root ID when the chain
// terminates on one of `roots`; returns 0 when the chain leads to a
// non-bot top-level comment (a user-only thread the assembler
// intentionally drops). Bounded by `maxReplyChainDepth` so a malformed
// payload with a self-referential reply cannot loop forever.
func walkToRoot(
	c forgeEntities.PullRequestComment,
	byID map[int64]forgeEntities.PullRequestComment,
	roots map[int64]*entities.ReviewThread,
) int64 {
	current := c
	for range maxReplyChainDepth {
		if current.InReplyToID == 0 {
			return 0
		}
		if _, ok := roots[current.InReplyToID]; ok {
			return current.InReplyToID
		}
		parent, ok := byID[current.InReplyToID]
		if !ok {
			return 0
		}
		current = parent
	}
	return 0
}

// maxReplyChainDepth bounds the InReplyToID walk so a malformed or
// hostile payload (a comment whose InReplyToID points at itself, or a
// cycle) cannot loop forever. 32 is comfortably above any realistic
// reply chain on a code review (the longest the team has seen in
// production is ~12).
const maxReplyChainDepth = 32

// IsBotAuthor returns a predicate the conversation walker uses to
// identify the bot's own comments. An author is the bot when EITHER:
//
//  1. it exactly (case-insensitively) equals one of the `identities`
//     the caller supplies — the account the deployment posts under
//     (a service-account login / email), wired from
//     `Settings.BotIdentities` and/or self-detected from the bot's own
//     PR-wide status annotations (see `DetectBotAuthors`); OR
//  2. it matches the built-in `code-guru` name shape (the default,
//     always recognised so GitHub App deployments need no config).
//
// The built-in shape match is **strict** so user names that merely
// contain `code-guru` as a substring (e.g. `code-guru-fan`,
// `alice+code-guru@example.com`) are NOT pulled into the conversation
// context as if they were prior bot findings. It matches when, after a
// case-insensitive comparison, the string starts with the literal
// `code-guru` AND the next character is one of:
//
//   - end of string (the bot identity is exactly `code-guru`)
//   - `[` — the GitHub App login shape (`code-guru[bot]`)
//   - `@` — the Azure DevOps PAT-identity shape (`code-guru@<tenant>`)
//
// Configured identities are matched by EXACT (case-insensitive) full
// equality rather than the prefix rule: they are complete account
// identities, so a substring/prefix match would risk pulling an
// unrelated user with a coincidentally overlapping name into the bot's
// conversation context.
//
// Returned as a closure (rather than a free function) so the caller
// can combine configured + self-detected identities per re-review
// without touching the assembler. Passing no identities preserves the
// pre-configuration behaviour byte-for-byte.
func IsBotAuthor(identities ...string) func(string) bool {
	const botMarker = "code-guru"
	// Copy the non-empty configured identities once so the returned
	// matcher does no allocation per call. `equalFold` already short-
	// circuits on a length mismatch, so the per-author scan is cheap
	// for the realistic 1-3 identity case.
	configured := make([]string, 0, len(identities))
	for _, id := range identities {
		if id != "" {
			configured = append(configured, id)
		}
	}
	return func(author string) bool {
		if author == "" {
			return false
		}
		for _, id := range configured {
			if equalFold(author, id) {
				return true
			}
		}
		if len(author) < len(botMarker) {
			return false
		}
		head := author[:len(botMarker)]
		if !equalFold(head, botMarker) {
			return false
		}
		if len(author) == len(botMarker) {
			return true
		}
		switch author[len(botMarker)] {
		case '[', '@':
			return true
		default:
			return false
		}
	}
}

// equalFold is `strings.EqualFold`-without-the-import — kept inline so
// the helper file has no transitive imports beyond what
// `BuildReviewConversation` already needs.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range len(a) {
		if foldByte(a[i]) != foldByte(b[i]) {
			return false
		}
	}
	return true
}

func foldByte(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b + ('a' - 'A')
	}
	return b
}
