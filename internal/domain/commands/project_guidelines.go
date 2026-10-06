package commands

import (
	"context"
	"strings"
	"time"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
	"github.com/rios0rios0/codeguru/internal/support"
)

// projectGuidelinesFileName is the file the bot loads from the reviewed
// repository as project-specific review context. `CLAUDE.md` at the repo
// root is where projects keep the conventions their own AI tooling must
// honour, which makes it exactly the document a code reviewer should
// read before judging the diff — naming rules, layering constraints,
// testing patterns, and the "why" behind non-obvious decisions that a
// generic ruleset cannot know about.
const projectGuidelinesFileName = "CLAUDE.md"

// The guidelines byte budget is operator-configurable
// (`ai.max_guidelines_bytes`) and resolved by
// `entities.AIConfig.GuidelinesBytes()`, which also documents the default
// and the small-context-window caveat. The cut is applied at load time
// (not prompt-build time) so every backend sees the same bounded content;
// `support.Truncate` appends its sentinel so the model can tell the
// document was cut rather than silently ending.

// projectGuidelinesFetchTimeout caps the provider file-content call. The
// guidelines are review-quality context, not a correctness gate — a hung
// provider must not stall the review pipeline behind a nice-to-have
// fetch. 10s is double the annotation-post SLO because a content GET on
// a large file is legitimately slower than a comment POST.
const projectGuidelinesFetchTimeout = 10 * time.Second

// loadProjectGuidelines fetches the reviewed repository's own root
// CLAUDE.md so the AI reviews the diff against the project's conventions,
// regardless of which provider (GitHub, Azure DevOps) hosts the PR.
//
// The load is skipped — returning "" so the prompt stays byte-for-byte
// identical to its pre-guidelines shape — when:
//
//   - the operator disabled the feature (`ai.project_guidelines: false`);
//   - the PR itself touches CLAUDE.md: the diff already carries the
//     changes for the model to read, and layering the pre-change copy on
//     top would show the model two conflicting versions of the same
//     document;
//   - the provider does not implement gitforge's FileAccessProvider
//     (no API file access — nothing to fetch from);
//   - the fetch fails or the file is missing/empty. Best-effort by
//     design: a repository without a CLAUDE.md is the common case, so
//     absence logs at debug and the review proceeds without guidelines.
func (c *ReviewCommand) loadProjectGuidelines(
	ctx context.Context,
	provider forgeEntities.ReviewProvider,
	repo forgeEntities.Repository,
	prID int,
	changedPaths []string,
	opts ReviewOptions,
) string {
	if !opts.LoadProjectGuidelines {
		return ""
	}
	if diffTouchesProjectGuidelines(changedPaths) {
		logger.Infof(
			"PR #%d: %s is part of the diff; the model reads it there, skipping the repository fetch",
			prID, projectGuidelinesFileName,
		)
		return ""
	}
	fap, ok := provider.(forgeEntities.FileAccessProvider)
	if !ok {
		logger.Debugf(
			"PR #%d: provider does not support API file access; reviewing without %s project guidelines",
			prID, projectGuidelinesFileName,
		)
		return ""
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, projectGuidelinesFetchTimeout)
	defer cancel()
	content, err := fap.GetFileContent(timeoutCtx, repo, projectGuidelinesFileName)
	if err != nil {
		// A missing file and a transient provider error are logged the
		// same way on purpose: neither should mark the review or the PR,
		// and gitforge does not expose a portable not-found sentinel to
		// tell them apart.
		logger.Debugf(
			"PR #%d: no %s loaded from the repository (%v); reviewing without project guidelines",
			prID, projectGuidelinesFileName, err,
		)
		return ""
	}

	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}

	// A zero budget means the caller never wired one (hand-built commands,
	// tests): fall back to the shipped default rather than truncating the
	// document to nothing.
	budget := opts.MaxGuidelinesBytes
	if budget <= 0 {
		budget = entities.AIConfig{}.GuidelinesBytes()
	}

	bounded := support.Truncate(trimmed, budget)
	// Compare the ORIGINAL length to the budget, never len(bounded) to
	// len(trimmed): `support.Truncate` appends a sentinel, so a document
	// just over budget produces a `bounded` that is LONGER than `trimmed`,
	// and a `len(bounded) < len(trimmed)` guard would miss those cuts
	// entirely. `len(trimmed) > budget` is exactly the condition Truncate
	// itself uses to decide whether to cut.
	if len(trimmed) > budget {
		logger.Warnf(
			"PR #%d: %s is %d byte(s) but the budget is %d; the model will review against a TRUNCATED "+
				"copy of the project's conventions — raise `ai.max_guidelines_bytes` if the window allows",
			prID, projectGuidelinesFileName, len(trimmed), budget,
		)
	}
	logger.Infof(
		"PR #%d: loaded %d byte(s) of project guidelines from the repository's %s",
		prID, len(bounded), projectGuidelinesFileName,
	)
	return bounded
}

// diffTouchesProjectGuidelines reports whether the PR's changed files
// include the root guidelines file. Paths are normalised with the same
// leading-slash rule used across this package so the ADO shape
// (`/CLAUDE.md`) matches, and compared case-insensitively so a rename to
// `claude.md` on a case-insensitive filesystem still counts as touching
// the document. Only the root file matters — a nested
// `docs/CLAUDE.md`-style path is not the repository's AI guidance file.
func diffTouchesProjectGuidelines(changedPaths []string) bool {
	for _, p := range changedPaths {
		if strings.EqualFold(normalizeFilePath(p), projectGuidelinesFileName) {
			return true
		}
	}
	return false
}
