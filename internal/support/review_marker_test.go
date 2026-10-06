package support_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/support"
)

func TestHasCompletedReviewMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bodies []string
		want   bool
	}{
		{
			name: "should match the review-complete marker body",
			bodies: []string{
				"✅ **Code Guru review complete.**\n\nVerdict: `approve` · 3 inline comments.\n",
			},
			want: true,
		},
		{
			name: "should match the review-failed marker body",
			bodies: []string{
				"⚠️ **Code Guru review failed.**\n\nThe AI step crashed.\n",
			},
			want: true,
		},
		{
			name: "should NOT match the in-flight reviewing marker (different prefix)",
			bodies: []string{
				"\U0001f916 **Code Guru is reviewing this PR.**\n\nPlease wait...",
			},
			// `**Code Guru is reviewing` does not contain the
			// `**Code Guru review` literal we look for (the space after
			// `Code Guru` is followed by `is`, not `review`). This is
			// intentional — the in-flight marker should NOT count as
			// "this PR has been reviewed".
			want: false,
		},
		{
			name:   "should return false on empty body list",
			bodies: nil,
			want:   false,
		},
		{
			name: "should return false when no body contains the marker",
			bodies: []string{
				"PR-wide comment from a user",
				"Looks good to me",
				"@code-guru please re-review",
			},
			want: false,
		},
		{
			name: "should match when the marker is one of several bodies",
			bodies: []string{
				"a user comment",
				"✅ **Code Guru review complete.** ...",
				"another user comment",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// given / when
			got := support.HasCompletedReviewMarker(tt.bodies)

			// then
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestHasMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		// identities mirrors `Settings.BotIdentities` — the accounts the
		// deployment posts under. A nil slice expands to zero variadic
		// args, so the built-in-token rows below behave exactly as they
		// did before mention matching became settings-aware.
		identities []string
		want       bool
	}{
		{
			name: "should match a plain @code-guru mention",
			body: "@code-guru please re-review the auth changes",
			want: true,
		},
		{
			name: "should match a case-insensitive mention",
			body: "Hey @Code-Guru, what about the timeouts?",
			want: true,
		},
		{
			name: "should match when followed by punctuation",
			body: "thanks @code-guru!",
			want: true,
		},
		{
			name: "should match when at end of string",
			body: "ping @code-guru",
			want: true,
		},
		{
			name: "should NOT match when extended into a longer identifier",
			body: "@code-guru-staging please confirm",
			want: false,
		},
		{
			name: "should NOT match @code-guru99 (digit continuation)",
			body: "@code-guru99 please re-review",
			want: false,
		},
		{
			name: "should keep scanning past a non-match and find a real mention",
			body: "@code-guru-staging deployed; @code-guru please re-review",
			want: true,
		},
		{
			name: "should return false when token absent",
			body: "@someone-else please re-review",
			want: false,
		},
		{
			name: "should return false on empty body",
			body: "",
			want: false,
		},
		// --- configured bot identities ---
		//
		// Note these rows deliberately avoid `code-guru[bot]`: `[` is a
		// word boundary, so the built-in token already matches inside
		// `@code-guru[bot]` on the pre-change code and such a row would
		// pass without proving anything. Every row below uses an identity
		// whose name does NOT start with `code-guru`.
		{
			name:       "should still match the built-in @code-guru when identities are configured",
			body:       "@code-guru please re-review",
			identities: []string{"svc-codeguru@corp.example"},
			want:       true,
		},
		{
			name:       "should match a configured identity by its short name",
			body:       "@svc-codeguru please re-review",
			identities: []string{"svc-codeguru@corp.example"},
			want:       true,
		},
		{
			name:       "should match a configured identity verbatim including its domain",
			body:       "@svc-codeguru@corp.example please re-review",
			identities: []string{"svc-codeguru@corp.example"},
			want:       true,
		},
		{
			name:       "should match a [bot]-suffixed identity by its short name",
			body:       "@deploybot please re-review",
			identities: []string{"deploybot[bot]"},
			want:       true,
		},
		{
			name:       "should lower-case the identity before matching",
			body:       "@svc-codeguru please re-review",
			identities: []string{"SVC-CodeGuru@Corp.EXAMPLE"},
			want:       true,
		},
		{
			name:       "should lower-case the identity before stripping the [bot] suffix",
			body:       "@deploybot please re-review",
			identities: []string{"DeployBot[BOT]"},
			want:       true,
		},
		{
			name:       "should strip one leading @ from the configured identity",
			body:       "@deploybot please re-review",
			identities: []string{"@deploybot"},
			want:       true,
		},
		{
			name:       "should match any one of several configured identities",
			body:       "@svc-b please re-review",
			identities: []string{"svc-a@corp.example", "svc-b@corp.example"},
			want:       true,
		},
		{
			name:       "should NOT match a derived token extended into a longer handle",
			body:       "@svc-codeguru-staging deployed",
			identities: []string{"svc-codeguru@corp.example"},
			want:       false,
		},
		{
			name:       "should NOT match when the configured identity is absent from the body",
			body:       "LGTM, thanks!",
			identities: []string{"svc-codeguru@corp.example"},
			want:       false,
		},
		{
			name:       "should NOT match an identity that is not configured",
			body:       "@svc-codeguru please re-review",
			identities: nil,
			want:       false,
		},
		{
			name:       "should ignore identities that are empty, blank, or only an @",
			body:       "@@ hello",
			identities: []string{"", "   ", "@"},
			want:       false,
		},
		{
			name:       "should drop a derived name shorter than the minimum length",
			body:       "reach me at x@a today",
			identities: []string{"a@b"},
			want:       false,
		},
		{
			name:       "should keep the verbatim identity when its short name is too short",
			body:       "@a@b please re-review",
			identities: []string{"a@b"},
			want:       true,
		},
		{
			name:       "should tolerate an identity that is only the [bot] suffix",
			body:       "nothing to see here",
			identities: []string{"[bot]"},
			want:       false,
		},
		{
			// Azure DevOps rewrites an @-autocompleted mention into
			// `@<identity-guid>` markup, so the typed name never reaches
			// the webhook payload. Pasting the GUID into `bot_identities`
			// makes the comment box's own form work.
			name:       "should match the Azure DevOps @<guid> markup when the GUID is configured",
			body:       "@<8f3a1e2b-0000-0000-0000-000000000000> please re-review",
			identities: []string{"8f3a1e2b-0000-0000-0000-000000000000"},
			want:       true,
		},
		{
			name:       "should NOT match an Azure DevOps @<guid> markup for an unconfigured identity",
			body:       "@<8f3a1e2b-0000-0000-0000-000000000000> please re-review",
			identities: []string{"svc-codeguru@corp.example"},
			want:       false,
		},
		{
			// Accepted trade-off, pinned deliberately: the scan has no
			// LEFT word-boundary check, so a derived short name also
			// matches inside an email host. Pre-existing behaviour
			// (`alice@code-guru.com` false-positives on the built-in
			// token today); fixing it would change `@code-guru`
			// semantics too and is out of scope here.
			name:       "should match a derived short name inside an email host (accepted false positive)",
			body:       "cc alice@svc-codeguru.corp.example",
			identities: []string{"svc-codeguru@corp.example"},
			want:       true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// given / when
			got := support.HasMention(tt.body, tt.identities...)

			// then
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestMentionTokensFor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		identities []string
		want       []string
	}{
		{
			name:       "should return nil when no identities are configured",
			identities: nil,
			want:       nil,
		},
		{
			name:       "should derive the [bot]-stripped and domain-stripped forms alongside the verbatim one",
			identities: []string{"code-guru[bot]", "svc-codeguru@corp.example"},
			// `@code-guru` is absent: it collapses onto the built-in
			// MentionToken, which HasMention always checks anyway.
			want: []string{
				"@code-guru[bot]", "@<code-guru[bot]>",
				"@svc-codeguru@corp.example", "@<svc-codeguru@corp.example>", "@svc-codeguru",
			},
		},
		{
			name:       "should derive the angle-bracket form Azure DevOps uses for an @-autocompleted mention",
			identities: []string{"8f3a1e2b-0000-0000-0000-000000000000"},
			want: []string{
				"@8f3a1e2b-0000-0000-0000-000000000000",
				"@<8f3a1e2b-0000-0000-0000-000000000000>",
			},
		},
		{
			name:       "should drop an identity that is exactly the built-in token",
			identities: []string{"code-guru"},
			want:       []string{"@<code-guru>"},
		},
		{
			name:       "should drop the built-in token regardless of case or a leading @",
			identities: []string{"@CODE-GURU"},
			want:       []string{"@<code-guru>"},
		},
		{
			name:       "should collapse duplicate identities into one token set",
			identities: []string{"svc@corp.example", "svc@corp.example"},
			want:       []string{"@svc@corp.example", "@<svc@corp.example>", "@svc"},
		},
		{
			name:       "should drop derived names shorter than the minimum length",
			identities: []string{"a@b"},
			want:       []string{"@a@b", "@<a@b>"},
		},
		{
			name:       "should skip identities that carry no usable name",
			identities: []string{"", "   ", "@"},
			want:       nil,
		},
		{
			name:       "should preserve input order across several identities",
			identities: []string{"svc-a@corp.example", "svc-b@corp.example"},
			want: []string{
				"@svc-a@corp.example", "@<svc-a@corp.example>", "@svc-a",
				"@svc-b@corp.example", "@<svc-b@corp.example>", "@svc-b",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// given / when
			got := support.MentionTokensFor(tt.identities)

			// then
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, "", "an empty token would hang the mention scan")
		})
	}
}

func TestContainsMentionToken(t *testing.T) {
	t.Parallel()

	t.Run("should return false for an empty token instead of looping forever", func(t *testing.T) {
		t.Parallel()

		// given: `strings.Index` returns 0 for the empty token, so without
		// the guard the scan slices `body[0:]` on every iteration and
		// never advances — the handler goroutine would spin at 100% CPU.
		// `mentionTokensFor` cannot emit an empty token today, but that
		// filter lives in another function, so the guard is pinned here.
		// A regression makes this test hang rather than fail.
		const body = "@code-guru please re-review"

		// when
		got := support.ContainsMentionToken(body, "")

		// then
		assert.False(t, got)
	})

	t.Run("should require a right-side word boundary", func(t *testing.T) {
		t.Parallel()

		// given / when
		matched := support.ContainsMentionToken("ping @svc-bot now", "@svc-bot")
		extended := support.ContainsMentionToken("ping @svc-botnet now", "@svc-bot")

		// then
		assert.True(t, matched)
		assert.False(t, extended)
	})
}

func TestDetectBotAuthors(t *testing.T) {
	t.Parallel()

	t.Run("should detect the bot from the author of a PR-wide review-complete annotation", func(t *testing.T) {
		t.Parallel()

		// given: a self-hosted deployment posts under a service account
		// whose name does not start with `code-guru`. Its PR-wide status
		// annotation carries the bot marker.
		comments := []forgeEntities.PullRequestComment{
			{
				ID:     1,
				Line:   0,
				Author: "automation@example.com",
				Body:   "✅ **Code Guru review complete.**\n\nVerdict: `approve`.",
			},
			{
				ID:       2,
				Line:     10,
				FilePath: "internal/foo.go",
				Author:   "automation@example.com",
				Body:     "[high] nil-check this",
			},
			{ID: 3, Line: 10, FilePath: "internal/foo.go", Author: "alice", Body: "already handled", InReplyToID: 2},
		}

		// when
		got := support.DetectBotAuthors(comments)

		// then
		assert.Equal(t, []string{"automation@example.com"}, got)
	})

	t.Run("should detect the bot from the reviewing / failed annotations too", func(t *testing.T) {
		t.Parallel()

		// given
		comments := []forgeEntities.PullRequestComment{
			{ID: 1, Line: 0, Author: "svc-bot", Body: "\U0001f916 **Code Guru is reviewing this PR.**"},
			{ID: 2, Line: 0, Author: "svc-bot", Body: "⚠️ **Code Guru review failed.**"},
		}

		// when
		got := support.DetectBotAuthors(comments)

		// then: both annotations are by the same account; collapsed once.
		assert.Equal(t, []string{"svc-bot"}, got)
	})

	t.Run("should ignore inline comments that merely quote the marker", func(t *testing.T) {
		t.Parallel()

		// given: a human pastes the bot's annotation text into an inline
		// comment. Only PR-wide (Line <= 0) annotations identify the bot.
		comments := []forgeEntities.PullRequestComment{
			{ID: 1, Line: 42, FilePath: "main.go", Author: "mallory", Body: "look: **Code Guru review complete.**"},
		}

		// when
		got := support.DetectBotAuthors(comments)

		// then
		assert.Empty(t, got)
	})

	t.Run("should ignore a PR-wide human comment that merely quotes the marker", func(t *testing.T) {
		t.Parallel()

		// given: a human discusses the annotation in a PR-wide comment.
		// The marker substring is present but preceded by ordinary text
		// (and a backtick), so the author must NOT be mistaken for the
		// bot — otherwise their inline threads would be pulled in as
		// prior bot threads and a re-review could auto-resolve them.
		comments := []forgeEntities.PullRequestComment{
			{
				ID:     1,
				Line:   0,
				Author: "alice",
				Body:   "should we reword `✅ **Code Guru review complete.**` to be shorter?",
			},
		}

		// when
		got := support.DetectBotAuthors(comments)

		// then
		assert.Empty(t, got)
	})

	t.Run("should ignore ordinary human PR-wide comments without the marker", func(t *testing.T) {
		t.Parallel()

		// given
		comments := []forgeEntities.PullRequestComment{
			{ID: 1, Line: 0, Author: "alice", Body: "@code-guru review it again"},
		}

		// when
		got := support.DetectBotAuthors(comments)

		// then
		assert.Empty(t, got)
	})

	t.Run("should return nil for no comments", func(t *testing.T) {
		t.Parallel()

		// given / when
		got := support.DetectBotAuthors(nil)

		// then
		assert.Nil(t, got)
	})
}

func TestExtractMentionedIdentityIDs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		body     string
		expected []string
	}{
		{
			name:     "should return nothing for an empty body",
			body:     "",
			expected: nil,
		},
		{
			name:     "should return nothing for a plain-text mention",
			body:     "@code-guru please take another look",
			expected: nil,
		},
		{
			name:     "should extract the identity id from Azure DevOps autocomplete markup",
			body:     "@<8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b> can you re-review?",
			expected: []string{"8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b"},
		},
		{
			name: "should lower-case the id so the comment box's upper-case markup matches the REST API's form",
			body: "@<8F3A1E2B-4C5D-6E7F-8A9B-0C1D2E3F4A5B> ",
			expected: []string{
				"8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b",
			},
		},
		{
			name: "should extract every distinct id when several accounts are mentioned",
			body: "@<8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b> @<11111111-2222-3333-4444-555555555555> ping",
			expected: []string{
				"8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b",
				"11111111-2222-3333-4444-555555555555",
			},
		},
		{
			name:     "should collapse a repeated id to a single entry",
			body:     "@<8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b> and again @<8F3A1E2B-4C5D-6E7F-8A9B-0C1D2E3F4A5B>",
			expected: []string{"8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b"},
		},
		{
			name:     "should ignore bracketed content that is not an identity id",
			body:     "see @<https://example.com/docs> and @<not-a-guid>",
			expected: nil,
		},
		{
			name:     "should ignore an unterminated bracket",
			body:     "@<8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b",
			expected: nil,
		},
		{
			name:     "should ignore a guid-length value carrying a non-hex character",
			body:     "@<8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5z>",
			expected: nil,
		},
		{
			name:     "should keep scanning after a rejected candidate",
			body:     "@<not-a-guid> then @<11111111-2222-3333-4444-555555555555>",
			expected: []string{"11111111-2222-3333-4444-555555555555"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// given
			body := tt.body

			// when
			ids := support.ExtractMentionedIdentityIDs(body)

			// then
			assert.Equal(t, tt.expected, ids)
		})
	}
}

func TestEqualIdentityID(t *testing.T) {
	t.Parallel()

	t.Run("should match ids that differ only in case", func(t *testing.T) {
		t.Parallel()

		// given
		resolvedID := "8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b"
		mentionedID := "8F3A1E2B-4C5D-6E7F-8A9B-0C1D2E3F4A5B"

		// when
		equal := support.EqualIdentityID(resolvedID, mentionedID)

		// then
		assert.True(t, equal, "Azure DevOps emits the same identity in both cases; the comparison must fold them")
	})

	t.Run("should not match different ids", func(t *testing.T) {
		t.Parallel()

		// given
		resolvedID := "8f3a1e2b-4c5d-6e7f-8a9b-0c1d2e3f4a5b"
		mentionedID := "11111111-2222-3333-4444-555555555555"

		// when
		equal := support.EqualIdentityID(resolvedID, mentionedID)

		// then
		assert.False(t, equal)
	})

	t.Run("should never match when the resolved identity is empty", func(t *testing.T) {
		t.Parallel()

		// given
		unresolved := ""

		// when
		equal := support.EqualIdentityID(unresolved, "")

		// then
		assert.False(t, equal, "an unresolved identity must not match an unparsed mention")
	})
}
