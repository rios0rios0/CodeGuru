package webhooks_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/rios0rios0/codeguru/internal/infrastructure/controllers/webhooks"
)

// These tests pin the hydration contract that compensates for the
// stripped-down `resource` block ADO **org-wide** subscriptions emit
// (only `{ url, pullRequestId }`, regardless of `resourceVersion`).
// Each scenario maps to a wire shape we have observed in production
// against subscriptions `fea3e13f-…` and `564b23d9-…`, so any future
// "let me clean up the hydrator" refactor must keep these green.

func TestIsSkinnyADOResource(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		resource webhooks.ADOResource
		want     bool
	}{
		{
			name: "should detect the org-wide skinny shape (only url + pullRequestId)",
			resource: webhooks.ADOResource{
				PullRequestID: 99999,
				URL:           "https://dev.azure.com/ExampleOrg/project-uuid-B/_apis/git/repositories/project-uuid-C/pullRequests/99999",
			},
			want: true,
		},
		{
			name: "should NOT flag a hydrated/full resource (repository.id present)",
			resource: webhooks.ADOResource{
				PullRequestID: 99999,
				URL:           "https://dev.azure.com/ExampleOrg/_apis/.../pullRequests/99999",
				Repository:    webhooks.ADORepository{ID: "project-uuid-C"},
			},
			want: false,
		},
		{
			name:     "should NOT flag a fully empty resource (no url, no id)",
			resource: webhooks.ADOResource{},
			want:     false,
		},
		{
			name: "should NOT flag a payload missing pullRequestId (defensive — webhook envelope without an id is malformed)",
			resource: webhooks.ADOResource{
				URL: "https://dev.azure.com/ExampleOrg/_apis/.../pullRequests/0",
			},
			want: false,
		},
		{
			name: "should NOT flag a payload whose url is whitespace-only",
			resource: webhooks.ADOResource{
				PullRequestID: 99999,
				URL:           "   ",
			},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// when
			got := webhooks.IsSkinnyADOResource(tc.resource)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestAppendAPIVersion(t *testing.T) {
	t.Parallel()

	t.Run("should append api-version on a URL without query", func(t *testing.T) {
		t.Parallel()

		// given
		raw := "https://dev.azure.com/ExampleOrg/_apis/git/repositories/abc/pullRequests/1"

		// when
		got, err := webhooks.AppendAPIVersion(raw, "7.1-preview.1")

		// then
		require.NoError(t, err)
		assert.Equal(t, raw+"?api-version=7.1-preview.1", got)
	})

	t.Run("should override an existing api-version query param", func(t *testing.T) {
		t.Parallel()

		// given
		raw := "https://dev.azure.com/ExampleOrg/_apis/git/pullRequests/1?api-version=5.0&foo=bar"

		// when
		got, err := webhooks.AppendAPIVersion(raw, "7.1-preview.1")

		// then
		require.NoError(t, err)
		assert.Contains(t, got, "api-version=7.1-preview.1")
		assert.Contains(t, got, "foo=bar")
		assert.NotContains(t, got, "api-version=5.0")
	})

	t.Run("should reject a relative URL (only an absolute one identifies the org)", func(t *testing.T) {
		t.Parallel()

		// given: a path-only input (no scheme/host). CLAUDE.md
		// requires the BDD `given/when/then` triplet on every
		// subtest; the input here is the precondition under test.

		// when
		got, err := webhooks.AppendAPIVersion("/_apis/git/pullRequests/1", "7.1-preview.1")

		// then
		require.Error(t, err)
		assert.Empty(t, got)
	})

	t.Run("should reject a URL with a control character that fails url.Parse", func(t *testing.T) {
		t.Parallel()

		// given: a URL ending in `\x7f` (DEL). `url.Parse` is
		// otherwise lenient — most strings parse — so a control
		// character is the canonical "make Go's parser actually
		// error" trigger. CLAUDE.md requires the BDD triplet on
		// every subtest even when the setup is trivial.

		// when
		got, err := webhooks.AppendAPIVersion("https://dev.azure.com/\x7f", "7.1-preview.1")

		// then
		require.Error(t, err)
		assert.Empty(t, got)
	})
}

func TestMergeHydratedADOResource(t *testing.T) {
	t.Parallel()

	t.Run("should prefer hydrated fields when both sides supply them", func(t *testing.T) {
		t.Parallel()

		// given
		original := webhooks.ADOResource{PullRequestID: 99999, URL: "https://orig"}
		hydrated := webhooks.ADOResource{
			PullRequestID: 99999,
			URL:           "https://hydrated",
			Status:        "active",
			Title:         "smoke",
			SourceRefName: "refs/heads/feat/x",
			TargetRefName: "refs/heads/main",
		}
		hydrated.Repository.ID = "e3555597"
		hydrated.Repository.Name = "catalog"
		hydrated.Repository.RemoteURL = "https://dev.azure.com/Org/Project/_git/catalog"
		hydrated.Repository.Project.Name = "backend"

		// when
		merged := webhooks.MergeHydratedADOResource(original, hydrated)

		// then
		assert.Equal(t, "https://hydrated", merged.URL)
		assert.Equal(t, "active", merged.Status)
		assert.Equal(t, "catalog", merged.Repository.Name)
		assert.Equal(t, "backend", merged.Repository.Project.Name)
	})

	t.Run("should fall back to original pullRequestId when hydrated body omitted it", func(t *testing.T) {
		t.Parallel()

		// given
		original := webhooks.ADOResource{PullRequestID: 99999, URL: "https://orig"}
		hydrated := webhooks.ADOResource{Status: "active"}
		hydrated.Repository.ID = "e3555597"

		// when
		merged := webhooks.MergeHydratedADOResource(original, hydrated)

		// then
		assert.Equal(t, 99999, merged.PullRequestID)
		assert.Equal(t, "https://orig", merged.URL)
		assert.Equal(t, "e3555597", merged.Repository.ID)
	})
}

// Hydrate end-to-end coverage: stand up a fake ADO API and verify the
// hydrator reaches it with the right Authorization header, parses the
// response, and surfaces non-2xx as an error.

func TestHTTPADOHydrator(t *testing.T) {
	t.Parallel()

	t.Run("should fetch and decode a full PR resource", func(t *testing.T) {
		t.Parallel()

		// given: the Authorization-header assertion runs inside the
		// handler goroutine so the test stays race-free under `-race`.
		// Capturing the header into a local variable for read in the test
		// goroutine is what triggered the data-race detector flagged on
		// PR #97 thread `PRRT_kwDOJKAEo85-5L63`.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			auth := r.Header.Get("Authorization")
			assert.NotEmpty(t, auth, "request must carry an Authorization header")
			assert.Contains(t, auth, "Basic ", "auth must use HTTP Basic per ADO PAT scheme")
			assert.Equal(t, "/ExampleOrg/_apis/git/pullRequests/99999", r.URL.Path)
			assert.Equal(t, "7.1-preview.1", r.URL.Query().Get("api-version"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"pullRequestId": 99999,
				"status": "active",
				"title": "smoke",
				"sourceRefName": "refs/heads/chore/smoke-test-6",
				"targetRefName": "refs/heads/main",
				"url": "https://dev.azure.com/ExampleOrg/_apis/git/repositories/abc/pullRequests/99999",
				"repository": {
					"id": "project-uuid-C",
					"name": "catalog",
					"remoteUrl": "https://dev.azure.com/ExampleOrg/backend/_git/catalog",
					"project": { "name": "backend" }
				}
			}`))
		}))
		defer server.Close()

		hydrator := webhooks.NewHTTPADOHydrator(routedTo(t, server))

		// when
		got, err := hydrator.Hydrate(
			context.Background(),
			"https://dev.azure.com/ExampleOrg/_apis/git/pullRequests/99999",
			"test-pat",
		)

		// then
		require.NoError(t, err)
		assert.Equal(t, 99999, got.PullRequestID)
		assert.Equal(t, "active", got.Status)
		assert.Equal(t, "catalog", got.Repository.Name)
		assert.Equal(t, "backend", got.Repository.Project.Name)
		assert.Equal(t, "refs/heads/chore/smoke-test-6", got.SourceRefName)
	})

	t.Run("should surface a non-2xx response as an error", func(t *testing.T) {
		t.Parallel()

		// given
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "forbidden", http.StatusForbidden)
		}))
		defer server.Close()

		hydrator := webhooks.NewHTTPADOHydrator(routedTo(t, server))

		// when
		_, err := hydrator.Hydrate(
			context.Background(),
			"https://dev.azure.com/ExampleOrg/_apis/git/pullRequests/1",
			"test-pat",
		)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "403")
	})

	t.Run("should reject an empty token", func(t *testing.T) {
		t.Parallel()

		// given
		hydrator := webhooks.NewHTTPADOHydrator(nil)

		// when
		_, err := hydrator.Hydrate(context.Background(), "https://dev.azure.com/_apis/git/pullRequests/1", "")

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "PAT is empty")
	})

	t.Run("should reject an empty resource URL", func(t *testing.T) {
		t.Parallel()

		// given
		hydrator := webhooks.NewHTTPADOHydrator(nil)

		// when
		_, err := hydrator.Hydrate(context.Background(), "", "test-pat")

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "URL is empty")
	})

	t.Run("should reject a malformed (relative) URL before issuing a request", func(t *testing.T) {
		t.Parallel()

		// given
		hydrator := webhooks.NewHTTPADOHydrator(nil)

		// when
		_, err := hydrator.Hydrate(context.Background(), "/_apis/git/pullRequests/1", "test-pat")

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed resource URL")
	})

	t.Run("should refuse to hydrate a non-ADO host (SSRF defence)", func(t *testing.T) {
		t.Parallel()

		// given: the production hydrator must reject any URL whose host
		// is not `dev.azure.com` or `*.visualstudio.com`. Without this
		// guard, an attacker who could forge a webhook delivery past the
		// source-IP / Basic-auth gate could trick the bot into making
		// a PAT-authenticated request to an attacker-controlled host —
		// the CodeQL `go/ssrf` finding on PR #97 thread `PRRT_kwDOJKAEo85-5Kvt`.
		hydrator := webhooks.NewHTTPADOHydrator(nil)

		// when
		_, err := hydrator.Hydrate(
			context.Background(),
			"https://attacker.example.com/_apis/git/pullRequests/1",
			"test-pat",
		)

		// then
		require.Error(t, err)
		assert.Contains(t, err.Error(), "non-ADO host")
	})
}

func TestIsADOAPIHost(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		url  string
		want bool
	}{
		{
			name: "should accept canonical https://dev.azure.com URL",
			url:  "https://dev.azure.com/Org/_apis/git/pullRequests/1",
			want: true,
		},
		{
			name: "should accept legacy *.visualstudio.com host",
			url:  "https://org.visualstudio.com/_apis/git/pullRequests/1",
			want: true,
		},
		{
			name: "should accept regional sub-domain on visualstudio.com",
			url:  "https://org.eu.visualstudio.com/_apis/git/pullRequests/1",
			want: true,
		},
		{
			name: "should accept dev.azure.com regardless of casing",
			url:  "https://DEV.AZURE.COM/Org/_apis/git/pullRequests/1",
			want: true,
		},
		{
			name: "should reject http (must be https)",
			url:  "http://dev.azure.com/Org/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject 127.0.0.1 (httptest.NewServer host)",
			url:  "http://127.0.0.1:42/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject an arbitrary attacker host",
			url:  "https://attacker.example.com/_apis/git/pullRequests/1",
			want: false,
		},
		{name: "should reject empty input", url: "", want: false},
		{name: "should reject a URL parse error (control character)", url: "https://dev.azure.com/\x7f", want: false},
		{
			name: "should reject github.com (would otherwise fail open if we check by suffix only)",
			url:  "https://github.com/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject user info that puts another host after an ADO-looking prefix",
			url:  "https://dev.azure.com@attacker.example.com/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject a host that only starts with dev.azure.com",
			url:  "https://dev.azure.com.attacker.example.com/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject an explicit port, which ADO REST URLs never carry",
			url:  "https://dev.azure.com:8443/Org/_apis/git/pullRequests/1",
			want: false,
		},
		{
			name: "should reject visualstudio.com without an organization",
			url:  "https://visualstudio.com/_apis/git/pullRequests/1",
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// when
			got := webhooks.IsADOAPIHost(tc.url)

			// then
			assert.Equal(t, tc.want, got)
		})
	}
}

// routedTo returns a client that delivers every request to server, whatever
// host its URL names, so a test drives the production hydrator against a fake
// Azure DevOps with its host check still in place.
func routedTo(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	require.NoError(t, err)
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			routed := request.Clone(request.Context())
			routed.URL.Scheme = target.Scheme
			routed.URL.Host = target.Host
			return http.DefaultTransport.RoundTrip(routed)
		}),
	}
}

// roundTripperFunc adapts a function to [http.RoundTripper].
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
