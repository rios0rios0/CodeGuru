package webhooks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ADOResourceHydrator fetches the full PR resource shape that org-wide ADO
// subscriptions strip down to `{ url, pullRequestId }`. The webhook handler
// calls it before the allowlist check whenever it sees the skinny shape, so
// downstream code keeps consuming the canonical `adoResource` block.
//
// The interface is small on purpose: the only thing tests need to substitute
// is "given this URL + token, return the resource." Everything else (HTTP
// client, retry policy) lives on the concrete implementation.
type ADOResourceHydrator interface {
	Hydrate(ctx context.Context, resourceURL, token string) (adoResource, error)
}

// adoAPIVersion is the api-version query parameter applied to the hydration
// request. `7.1-preview.1` is the lowest version that returns the full PR
// envelope including `repository.project.name` and the source/target refs we
// need for the worker queue. Older versions (`5.0`, `6.0`) return a thinner
// shape that still omits `project.name` — verified against
// `https://learn.microsoft.com/en-us/rest/api/azure/devops/git/pull-requests/get-pull-request`.
const adoAPIVersion = "7.1-preview.1"

// adoHydrationTimeout caps the per-request wall time of the hydration GET.
// Hydration runs on the HTTP handler path before work is enqueued, with
// net/http already serving each incoming webhook in its own goroutine. 10 s
// is long enough for the p99 latency we see from the in-cluster hop to
// dev.azure.com without letting a partial outage accumulate too many stuck
// request goroutines.
const adoHydrationTimeout = 10 * time.Second

// httpADOHydrator is the production hydrator. It performs a single GET
// against the resource URL using the ADO PAT (Basic auth with empty
// username, PAT as password — the documented scheme for personal access
// tokens). Every URL passes `isADOAPIHost` first, tests included: they
// drive it against `httptest.NewServer` through a client whose transport
// delivers the request there, never by relaxing the check.
type httpADOHydrator struct {
	client *http.Client
}

// NewHTTPADOHydrator returns a hydrator that uses the supplied HTTP client.
// Pass nil to use a sensible default (a per-call timeout is still applied
// via [context.WithTimeout], so this client doesn't need its own).
func NewHTTPADOHydrator(client *http.Client) ADOResourceHydrator {
	if client == nil {
		client = &http.Client{Timeout: adoHydrationTimeout}
	}
	return &httpADOHydrator{client: client}
}

// Hydrate fetches the full PR resource at resourceURL using the PAT.
func (h *httpADOHydrator) Hydrate(ctx context.Context, resourceURL, token string) (adoResource, error) {
	if resourceURL == "" {
		return adoResource{}, errors.New("resource URL is empty")
	}
	if token == "" {
		return adoResource{}, errors.New("ADO PAT is empty")
	}

	hydrationURL, err := appendAPIVersion(resourceURL, adoAPIVersion)
	if err != nil {
		return adoResource{}, fmt.Errorf("malformed resource URL: %w", err)
	}
	if !isADOAPIHost(hydrationURL) {
		return adoResource{}, fmt.Errorf("refusing to hydrate non-ADO host in %q", hydrationURL)
	}

	ctx, cancel := context.WithTimeout(ctx, adoHydrationTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hydrationURL, http.NoBody)
	if err != nil {
		return adoResource{}, fmt.Errorf("build hydration request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(":"+token)))

	resp, err := h.client.Do(req)
	if err != nil {
		return adoResource{}, fmt.Errorf("hydration GET: %w", err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return adoResource{}, fmt.Errorf("hydration GET returned %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return adoResource{}, fmt.Errorf("read hydration body: %w", err)
	}

	var full adoResource
	if decodeErr := json.Unmarshal(body, &full); decodeErr != nil {
		return adoResource{}, fmt.Errorf("decode hydration body: %w", decodeErr)
	}
	return full, nil
}

// appendAPIVersion adds `?api-version=...` to the URL, preserving any
// existing query string. ADO resource URLs from webhook payloads do not
// carry a query, but we round-trip through `url.Parse` to stay safe against
// future schema changes.
func appendAPIVersion(raw, version string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("not an absolute URL: %q", raw)
	}
	q := u.Query()
	q.Set("api-version", version)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// adoAPIURL matches the URLs Azure DevOps serves its REST API from: https,
// then `dev.azure.com` or an organization's `*.visualstudio.com` host
// (regional ones such as `org.eu.visualstudio.com` included), then a path
// free of control characters, which no URL may carry. The host has to follow
// the scheme directly and the path the host, so neither user info nor a port
// can bring another host or service in.
var adoAPIURL = regexp.MustCompile(
	`(?i)^https://(?:dev\.azure\.com|(?:[a-z0-9-]+\.)+visualstudio\.com)/[^\x00-\x1f\x7f]*$`,
)

// isADOAPIHost validates that a URL points at an Azure DevOps host before
// the hydrator dispatches an authenticated request to it. The webhook
// payload's `resource.url` is operator-untrusted data — anyone able to
// forge a delivery past the source-IP / Basic-auth gate could otherwise
// trick the bot into making PAT-authenticated requests to an attacker-
// controlled host (CodeQL `go/request-forgery`). Only the two host shapes
// ADO actually uses for REST endpoints pass: `dev.azure.com` and
// `*.visualstudio.com` (mirroring the org-extraction logic). It is one
// pattern match on the URL, returned as it is: CodeQL follows a check to the
// request only through a function that returns the check alone.
func isADOAPIHost(rawURL string) bool {
	return adoAPIURL.MatchString(rawURL)
}

// mergeHydratedADOResource combines the original webhook resource with the
// hydrated payload, preferring hydrated values for every field the API
// populated. The original `pullRequestId` and `url` are guaranteed to be
// present in the skinny shape and act as fallbacks if the hydrated response
// somehow elided them — defence-in-depth against future ADO schema drift.
func mergeHydratedADOResource(original, hydrated adoResource) adoResource {
	if hydrated.PullRequestID == 0 {
		hydrated.PullRequestID = original.PullRequestID
	}
	if hydrated.URL == "" {
		hydrated.URL = original.URL
	}
	return hydrated
}

// isSkinnyADOResource returns true when the webhook payload's `resource`
// block lacks the repository envelope. ADO **org-wide** subscriptions emit
// this skinny shape regardless of `resourceVersion` or `messagesToSend`
// — empirically captured on subscriptions
// `subscription-A` and
// `subscription-B`, where 40/40 deliveries arrived
// with `resource = { pullRequestId, url }` only. The handler treats this
// shape as a hint to call out to the ADO REST API.
func isSkinnyADOResource(r adoResource) bool {
	return r.Repository.ID == "" &&
		strings.TrimSpace(r.URL) != "" &&
		r.PullRequestID > 0
}
