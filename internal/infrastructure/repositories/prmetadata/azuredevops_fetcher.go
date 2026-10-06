package prmetadata

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	logger "github.com/sirupsen/logrus"

	forgeEntities "github.com/rios0rios0/gitforge/v4/pkg/global/domain/entities"

	"github.com/rios0rios0/codeguru/internal/domain/entities"
)

// adoAPIBaseURL is the fixed Azure DevOps REST host. Constant so a
// crafted repository entity can never point an authenticated request
// at another host — the same SSRF posture as the webhook hydrator's
// host allowlist, enforced here by construction instead of validation.
const adoAPIBaseURL = "https://dev.azure.com"

// adoAPIVersion matches the api-version gitforge's Azure DevOps
// provider pins for its pull request calls, so both codepaths observe
// the same wire shapes.
const adoAPIVersion = "7.0"

// AzureDevOpsFetcher reads PR metadata from the Azure DevOps REST API.
// Unlike GitHub, ADO splits the data across two endpoints: the PR
// resource carries `description`, and the commit count comes from the
// PR's `/commits` collection.
type AzureDevOpsFetcher struct {
	client  *http.Client
	baseURL string
}

// NewAzureDevOpsFetcher creates an ADO metadata fetcher using the
// supplied HTTP client. Pass nil to use a default client with the
// package fetch timeout.
func NewAzureDevOpsFetcher(client *http.Client) *AzureDevOpsFetcher {
	if client == nil {
		client = &http.Client{Timeout: fetchTimeout}
	}
	return &AzureDevOpsFetcher{client: client, baseURL: adoAPIBaseURL}
}

// WithBaseURL overrides the API base URL. Test seam only — production
// code always talks to dev.azure.com.
func (f *AzureDevOpsFetcher) WithBaseURL(baseURL string) *AzureDevOpsFetcher {
	f.baseURL = baseURL
	return f
}

// GetPullRequestMetadata fetches the PR description and commit count.
// The description read is authoritative — its failure fails the fetch.
// The commit-count read degrades gracefully: on error the description
// is still returned (with CommitCount 0, which the prompt renders as
// "unknown" by omitting the commit line).
func (f *AzureDevOpsFetcher) GetPullRequestMetadata(
	ctx context.Context,
	token string,
	repo forgeEntities.Repository,
	prID int,
) (entities.PullRequestMetadata, error) {
	prURL := fmt.Sprintf("%s?api-version=%s", f.pullRequestURL(repo, prID), adoAPIVersion)
	var prPayload struct {
		Description string `json:"description"`
	}
	if err := f.getJSON(ctx, token, prURL, &prPayload); err != nil {
		return entities.PullRequestMetadata{}, fmt.Errorf("ADO PR metadata: %w", err)
	}

	metadata := entities.PullRequestMetadata{Description: prPayload.Description}

	commitsURL := fmt.Sprintf("%s/commits?api-version=%s", f.pullRequestURL(repo, prID), adoAPIVersion)
	var commitsPayload struct {
		Count int `json:"count"`
	}
	if err := f.getJSON(ctx, token, commitsURL, &commitsPayload); err != nil {
		logger.Debugf("ADO PR #%d: commit count unavailable (%v); returning description-only metadata", prID, err)
		return metadata, nil
	}
	metadata.CommitCount = commitsPayload.Count

	return metadata, nil
}

// pullRequestURL builds the PR resource URL, mirroring gitforge's ADO
// provider conventions: organization reduced to its first path segment
// and the repository addressed by ID when known, name otherwise. Every
// caller-influenced segment is path-escaped.
func (f *AzureDevOpsFetcher) pullRequestURL(repo forgeEntities.Repository, prID int) string {
	organization := url.PathEscape(strings.Split(repo.Organization, "/")[0])
	repoIdentifier := repo.ID
	if repoIdentifier == "" {
		repoIdentifier = repo.Name
	}
	return fmt.Sprintf(
		"%s/%s/%s/_apis/git/repositories/%s/pullrequests/%d",
		f.baseURL, organization, url.PathEscape(repo.Project), url.PathEscape(repoIdentifier), prID,
	)
}

// getJSON performs an authenticated GET and decodes the JSON response
// into out. Auth follows the documented ADO PAT scheme (Basic with an
// empty username), matching the webhook hydrator.
func (f *AzureDevOpsFetcher) getJSON(ctx context.Context, token, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+token)))
	}

	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("GET: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("GET returned %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}
	if unmarshalErr := json.Unmarshal(body, out); unmarshalErr != nil {
		return fmt.Errorf("decode body: %w", unmarshalErr)
	}
	return nil
}
