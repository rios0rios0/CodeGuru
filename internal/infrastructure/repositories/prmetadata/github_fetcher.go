package prmetadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// githubAPIBaseURL is the fixed REST endpoint for github.com. Constant
// (not derived from any payload) so an attacker-influenced repository
// entity can never redirect an authenticated request to another host —
// the same SSRF posture as the GitHub App installation-token exchange.
const githubAPIBaseURL = "https://api.github.com"

// githubAPIVersion pins the REST API revision, mirroring the
// installation-token exchange. GitHub recommends sending it explicitly
// so a future default-version bump cannot change response shapes
// underneath us.
const githubAPIVersion = "2022-11-28"

// GitHubFetcher reads PR metadata from the GitHub REST API. A single
// `GET /repos/{owner}/{repo}/pulls/{number}` returns both the PR body
// and the total commit count, so this vendor needs exactly one call.
type GitHubFetcher struct {
	client  *http.Client
	baseURL string
}

// NewGitHubFetcher creates a GitHub metadata fetcher using the supplied
// HTTP client. Pass nil to use a default client with the package fetch
// timeout.
func NewGitHubFetcher(client *http.Client) *GitHubFetcher {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	return &GitHubFetcher{client: client, baseURL: githubAPIBaseURL}
}

// WithBaseURL overrides the API base URL. Test seam only — production
// code always talks to api.github.com. Mirrors the `WithEndpoint`
// option on the Anthropic backend.
func (f *GitHubFetcher) WithBaseURL(baseURL string) *GitHubFetcher {
	f.baseURL = baseURL
	return f
}

// GetPullRequestMetadata fetches the PR's description (`body`) and
// commit count (`commits`) in one REST call.
func (f *GitHubFetcher) GetPullRequestMetadata(
	ctx context.Context,
	token string,
	repo forgeEntities.Repository,
	prID int,
) (entities.PullRequestMetadata, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/pulls/%d",
		f.baseURL, url.PathEscape(repo.Organization), url.PathEscape(repo.Name), prID,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return entities.PullRequestMetadata{}, fmt.Errorf("build GitHub PR metadata request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-Github-Api-Version", githubAPIVersion)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return entities.PullRequestMetadata{}, fmt.Errorf("GitHub PR metadata GET: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return entities.PullRequestMetadata{}, fmt.Errorf("GitHub PR metadata GET returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return entities.PullRequestMetadata{}, fmt.Errorf("read GitHub PR metadata body: %w", err)
	}

	var payload struct {
		Body    string `json:"body"`
		Commits int    `json:"commits"`
	}
	if unmarshalErr := json.Unmarshal(body, &payload); unmarshalErr != nil {
		return entities.PullRequestMetadata{}, fmt.Errorf("decode GitHub PR metadata body: %w", unmarshalErr)
	}

	return entities.PullRequestMetadata{
		Description: payload.Body,
		CommitCount: payload.Commits,
	}, nil
}
