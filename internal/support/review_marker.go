package support

import (
	"strings"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"
)

// botReviewCompleteMarker is the unique substring the bot writes into
// every "review complete" / "review failed" PR-wide annotation. The
// prefix is shared across both annotation bodies (see
// `commands.buildReviewCompleteBody` and `commands.buildReviewFailedBody`)
// so a single substring match catches both shapes — meaning a previous
// successful review AND a previous failed review both count as "this
// PR has already been touched, do not re-review on the next push".
//
// The "is reviewing" marker (`commands.buildReviewingMarkerBody`) is
// intentionally NOT in this set: it signals an in-flight review, not a
// completed one, so a webhook arriving while another pod is still
// reviewing should still go through the K8s-Lease cross-pod dedup
// rather than the marker gate.
const botReviewCompleteMarker = "**Code Guru review"

// HasCompletedReviewMarker returns true when any of the supplied
// PR-wide comment bodies looks like the bot's "review complete" or
// "review failed" annotation. The check is intentionally substring-
// based (not exact-match) so a future tweak to the annotation body
// does not accidentally bypass the gate; the marker is rare enough
// in real human comments that the false-positive risk is acceptable.
//
// Used by `ReviewCommand.Execute` as the review-once-per-PR gate:
// when this returns true and the user did NOT mention the bot, the
// review is short-circuited so the PR is not flooded with duplicate
// reviews on every push.
func HasCompletedReviewMarker(bodies []string) bool {
	for _, body := range bodies {
		if strings.Contains(body, botReviewCompleteMarker) {
			return true
		}
	}
	return false
}

// botAnnotationMarker is the substring shared by ALL of the bot's
// PR-wide status annotations: the "is reviewing"
// (`buildReviewingMarkerBody`), "review complete"
// (`buildReviewCompleteBody`), and "review failed"
// (`buildReviewFailedBody`) bodies all render `**Code Guru <...>`.
// Broader than `botReviewCompleteMarker` (which deliberately matches
// only the completed/failed shapes for the review-once gate): here we
// want to recognise the bot from ANY status annotation it has posted,
// including the "reviewing" marker that exists when a review crashed
// before completing.
const botAnnotationMarker = "**Code Guru "

// DetectBotAuthors returns the distinct set of comment authors that
// posted one of the bot's PR-wide status annotations on this PR (the
// "reviewing" / "review complete" / "review failed" notices, all of
// which carry the botAnnotationMarker substring).
//
// It exists so the re-review conversation walk can recognise the bot's
// own prior comments REGARDLESS of the account name the deployment
// posts under. On GitHub the bot is `code-guru[bot]` and the built-in
// matcher in `IsBotAuthor` recognises it, but a self-hosted Azure
// DevOps deployment commonly posts under an organisation service
// account (e.g. an `automation` / `svc-*` identity) whose name does
// not start with `code-guru`. Without self-detection,
// `BuildReviewConversation` finds zero prior bot threads, the LLM
// re-reviews from scratch on every re-review, and the bot re-posts
// findings the PR author has already addressed or rebutted.
//
// Only PR-wide comments (`Line <= 0`) carrying the annotation marker
// are considered, so a human who merely quotes the bot inline is not
// mis-identified. The returned identities are suitable to pass straight
// to `IsBotAuthor` as additional bot identities. Order is the
// first-seen order of the input; duplicates are collapsed.
func DetectBotAuthors(comments []forgeEntities.PullRequestComment) []string {
	seen := make(map[string]struct{})
	var authors []string
	for _, comment := range comments {
		if comment.Line > 0 || comment.Author == "" {
			continue
		}
		if !looksLikeBotAnnotation(comment.Body) {
			continue
		}
		if _, ok := seen[comment.Author]; ok {
			continue
		}
		seen[comment.Author] = struct{}{}
		authors = append(authors, comment.Author)
	}
	return authors
}

// looksLikeBotAnnotation reports whether body is one of the bot's own
// PR-wide status annotations rather than a human comment that merely
// quotes or discusses the marker. All three annotation bodies open with
// a single status emoji, a space, then the bold marker
// (`🤖/✅/⚠️ **Code Guru …`), so the marker sits at the very START of the
// body modulo that leading emoji/whitespace decoration.
//
// The detector therefore requires `botAnnotationMarker` to appear with
// nothing but decoration before it — specifically no ASCII letter and no
// backtick. A human PR-wide comment that discusses the annotation
// (e.g. "should we reword `✅ **Code Guru review complete.**`?") always
// has letters and/or a backtick before the marker, so it no longer trips
// the detector; without this anchor, that human would be mis-identified
// as the bot and their inline threads pulled in as prior bot threads —
// which a re-review could then auto-reply to and resolve. Anchoring on
// the marker (rather than hard-coding each annotation's exact emoji +
// wording) keeps the check resilient to future tweaks of the annotation
// bodies, the same rationale `botReviewCompleteMarker` documents. Pinned
// per Copilot review on PR #163.
func looksLikeBotAnnotation(body string) bool {
	before, _, ok := strings.Cut(body, botAnnotationMarker)
	if !ok {
		return false
	}
	for _, r := range before {
		if r == '`' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// MentionToken is the built-in literal the bot looks for in a user's PR
// comment to treat the comment as a re-review request. Case-insensitive
// match is performed by HasMention; word-boundary checks prevent a
// substring match against unrelated `@code-guru-foo` mentions.
//
// This is the FLOOR, not the whole trigger set: HasMention additionally
// accepts mentions of the account the deployment actually posts under,
// derived from `Settings.BotIdentities` (see mentionTokensFor).
const MentionToken = "@code-guru"

// minMentionNameLength is the shortest account name a derived mention
// token may carry (excluding the leading `@`). Derivation is automatic —
// an operator writing `x@corp.example` in `bot_identities` is naming the
// account, not asking for `@x` to summon the bot — so single-character
// names are dropped rather than turned into a trigger that would fire on
// almost any comment.
const minMentionNameLength = 2

// botLoginSuffix is the suffix GitHub appends to a GitHub App's login
// (`code-guru[bot]`). Mentions never carry it: a user types
// `@code-guru`, never `@code-guru[bot]`, so it is stripped when deriving
// a mention token from a configured identity.
const botLoginSuffix = "[bot]"

// HasMention returns true when the comment body mentions the bot. Two
// kinds of mention count:
//
//  1. the built-in `@code-guru` token (always accepted, so a deployment
//     with no configuration keeps working exactly as before); and
//  2. any account the deployment posts under, supplied by the caller
//     from `Settings.BotIdentities` and normalised by mentionTokensFor.
//
// The second form exists because a deployment posting under a GitHub App
// (`code-guru[bot]`) or an organisation service account
// (`svc-codeguru@corp.example`) is @-mentioned by the name users see on
// the PR — which used to match nothing at all, leaving the bot silent.
//
// Case-insensitive; rejects substrings that continue past the token
// (e.g. `@code-guru-bot` is NOT a match because the next byte is not
// whitespace / punctuation / EOF).
func HasMention(body string, identities ...string) bool {
	lower := strings.ToLower(body)
	if containsMentionToken(lower, MentionToken) {
		return true
	}
	for _, token := range mentionTokensFor(identities) {
		if containsMentionToken(lower, token) {
			return true
		}
	}
	return false
}

// containsMentionToken reports whether token appears in lower as a
// standalone mention rather than as the head of a longer handle.
//
// `lower` MUST already be lower-cased by the caller (HasMention does it
// once for every token); isMentionWordChar has no `A`-`Z` branch, so a
// raw body would silently lose case-insensitivity with no compile error.
//
// The empty token is rejected up front: `strings.Index` returns 0 for it,
// the slice below would never advance, and the loop would spin forever.
// The minimum-length filter that makes this unreachable today lives in
// deriveMentionNames — a different function — so the guard stays here.
func containsMentionToken(lower, token string) bool {
	if token == "" {
		return false
	}
	for {
		idx := strings.Index(lower, token)
		if idx == -1 {
			return false
		}
		end := idx + len(token)
		// Word-boundary check on the right side: the next byte must be
		// whitespace, punctuation, or end-of-string. Letters / digits /
		// `-` / `_` mean the match is part of a longer identifier (e.g.
		// `@code-guru-staging`) and we should keep scanning.
		if end == len(lower) || !isMentionWordChar(lower[end]) {
			return true
		}
		lower = lower[end:]
	}
}

// mentionTokensFor turns the configured bot identities into the extra
// `@`-prefixed tokens HasMention scans for, in stable input order with
// duplicates (and anything equal to MentionToken) collapsed.
//
// Returns nil for an empty input so the no-configuration path — every
// webhook comment on a default deployment — allocates nothing.
func mentionTokensFor(identities []string) []string {
	if len(identities) == 0 {
		return nil
	}
	seen := map[string]struct{}{MentionToken: {}}
	var tokens []string
	for _, identity := range identities {
		for _, name := range deriveMentionNames(identity) {
			token := "@" + name
			if _, ok := seen[token]; ok {
				continue
			}
			seen[token] = struct{}{}
			tokens = append(tokens, token)
		}
	}
	return tokens
}

// deriveMentionNames expands one configured account identity into every
// name a user might plausibly type to mention it, lower-cased and
// without the leading `@`.
//
// An identity is an ACCOUNT (`code-guru[bot]`, `svc-codeguru@corp.example`)
// while a mention is what a human types (`@code-guru`, `@svc-codeguru`),
// and the two differ on both platforms — hence four candidates:
//
//   - the identity verbatim;
//   - the identity wrapped in angle brackets, the markup Azure DevOps
//     substitutes for an @-autocompleted mention (`@<identity-guid>`), so
//     an operator can paste the bot's ADO identity GUID straight into
//     `bot_identities` and have the comment box's own form match;
//   - without a trailing `[bot]`, the GitHub App shape; and
//   - the part before `@`, the Azure DevOps UPN / service-account shape.
//
// `strings.ToLower` runs BEFORE the `[bot]` strip so a `Code-Guru[BOT]`
// identity still collapses onto the built-in token; reordering the two
// silently breaks that.
//
// The verbatim candidate is usually redundant — isMentionWordChar treats
// `[` and `@` as boundaries, so the shorter candidates already match
// inside the longer form — and the bracketed one is meaningless for a
// non-GUID identity. Both are kept unconditionally rather than guessing
// at an identity's format: each costs one extra scan of the comment
// body, and neither can match text a human would plausibly type.
func deriveMentionNames(identity string) []string {
	base := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(identity)), "@")
	if base == "" {
		return nil
	}
	candidates := []string{base, "<" + base + ">"}
	if short, ok := strings.CutSuffix(base, botLoginSuffix); ok {
		candidates = append(candidates, short)
		base = short
	}
	if idx := strings.IndexByte(base, '@'); idx > 0 {
		candidates = append(candidates, base[:idx])
	}
	names := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidate) >= minMentionNameLength {
			names = append(names, candidate)
		}
	}
	return names
}

// isMentionWordChar reports whether b is a character that would be part
// of a longer mention identifier. The set matches GitHub / Azure DevOps
// mention rules: alphanumerics, `-`, `_`. Anything else is a word
// boundary and the preceding token counts as a real mention.
//
// There is no `A`-`Z` branch because every caller lower-cases the body
// first — see containsMentionToken.
func isMentionWordChar(b byte) bool {
	switch {
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '-' || b == '_':
		return true
	default:
		return false
	}
}

// adoMentionIDPrefix opens the markup Azure DevOps substitutes for an
// @-autocompleted mention. When a user picks an account out of the ADO
// comment box's autocomplete, the stored comment body carries
// `@<identity-guid>` — the account's identity GUID, NOT the name the
// user saw and picked. So a `bot_identities` entry written as an
// account name (`svc-codeguru@corp.example`) cannot possibly match the
// body, and the mention silently reaches no one: the most natural way
// to summon the bot on Azure DevOps is precisely the way that used to
// fail. `HasMention` handles the case where an operator pastes the
// GUID into `bot_identities` (see deriveMentionNames' bracketed
// candidate); this extractor exists so the ADO webhook handler can
// close the gap with no configuration at all, by comparing the ids
// against the identity its own PAT authenticates as.
const adoMentionIDPrefix = "@<"

// identityIDLength is the length of a canonical 8-4-4-4-12 GUID. Used
// to reject anything inside `@<…>` that is not an identity id, so a
// comment containing e.g. an autolinked `@<https://example.com>` never
// triggers an identity lookup (or a log line) on the webhook path.
const identityIDLength = 36

// ExtractMentionedIdentityIDs returns the Azure DevOps identity ids
// @-mentioned in body through the comment box's autocomplete markup
// (`@<identity-guid>`), lower-cased and de-duplicated in first-seen
// order. Returns nil when the body carries no such markup — which is
// the overwhelmingly common case, so the scan allocates nothing on the
// hot path.
//
// Only well-formed GUIDs are returned (see IsIdentityID): the
// caller uses a non-empty result BOTH as the trigger for an outbound
// identity lookup and as operator-facing log content, and neither
// should be reachable by arbitrary text a commenter can type.
func ExtractMentionedIdentityIDs(body string) []string {
	var ids []string
	var seen map[string]struct{}
	rest := body
	for {
		idx := strings.Index(rest, adoMentionIDPrefix)
		if idx == -1 {
			return ids
		}
		rest = rest[idx+len(adoMentionIDPrefix):]
		end := strings.IndexByte(rest, '>')
		if end == -1 {
			return ids
		}
		candidate := rest[:end]
		rest = rest[end+1:]
		// Length first: `@<` also opens autolinked URLs and other markup,
		// and lower-casing those before the shape check would copy every
		// one of them on the webhook path for nothing.
		if len(candidate) != identityIDLength {
			continue
		}
		candidate = strings.ToLower(candidate)
		if !IsIdentityID(candidate) {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		if seen == nil {
			seen = map[string]struct{}{}
		}
		seen[candidate] = struct{}{}
		ids = append(ids, candidate)
	}
}

// IsIdentityID reports whether s is a canonical lower-case 8-4-4-4-12
// hyphenated GUID. Deliberately stricter than `uuid.Parse`: the braced
// and unhyphenated forms never appear in ADO mention markup, and every
// value that passes here is safe to embed in a log line verbatim —
// which is why the identity resolver validates the id it reads back
// from the REST API with it too, rather than trusting the response.
func IsIdentityID(s string) bool {
	if len(s) != identityIDLength {
		return false
	}
	for i := range identityIDLength {
		c := s[i]
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// EqualIdentityID compares two identity ids case-insensitively. Azure
// DevOps is inconsistent about GUID case across its own surfaces — the
// comment box emits the mention markup upper-cased while the REST API
// returns the same identity lower-cased — so a byte comparison of the
// two would never match. Empty ids never compare equal, so an
// unresolved identity cannot match an unparsed mention.
func EqualIdentityID(a, b string) bool {
	return a != "" && equalFold(a, b)
}
