//nolint:gochecknoglobals // Test-only re-exports: a package-level var IS the mechanism (a method value or function value cannot be a const).
package webhooks

import (
	"net/http"
	"time"
)

// Re-exports of the unexported parsing/normalisation helpers in the ADO
// handler so the external `webhooks_test` package can pin their contracts
// directly — the integration tests on `HandleAzureDevOps` cover the happy
// paths, but each helper has its own surface area of edge cases (URL
// shapes ADO actually delivers, status normalisation, ref prefixes) that
// deserves dedicated coverage.
//
// The variable indirection keeps the production identifiers unexported —
// nothing outside a test ever mentions these names, and `_test.go` files
// are excluded from non-test builds by the toolchain itself.
var (
	ExtractADOOrganization       = extractADOOrganization
	IsClosedADOPullRequestStatus = isClosedADOPullRequestStatus
	IsSupportedADOEvent          = isSupportedADOEvent
	RefToBranch                  = refToBranch
	IsSkinnyADOResource          = isSkinnyADOResource
	AppendAPIVersion             = appendAPIVersion
	IsADOAPIHost                 = isADOAPIHost
	EscapeLineBreaks             = escapeLineBreaks
	DeliveryFields               = deliveryFields
	LogDebugf                    = logDebugf
	LogInfof                     = logInfof
	LogWarnf                     = logWarnf
)

// NewTestHTTPADOIdentityResolver returns the production identity
// resolver pointed at an arbitrary base URL, so tests can drive it
// against `httptest.NewServer` instead of the constant
// `https://dev.azure.com` host it pins in production (that pin is the
// path's SSRF defence, so the override lives in `export_test.go` and
// never reaches a non-test build).
func NewTestHTTPADOIdentityResolver(client *http.Client, baseURL string) ADOIdentityResolver {
	if client == nil {
		client = &http.Client{Timeout: adoIdentityTimeout}
	}
	return &httpADOIdentityResolver{
		client:            client,
		baseURL:           baseURL,
		endpointValidator: func(string) bool { return true },
		cache:             map[string]string{},
	}
}

// ADOResource / ADORepository / ADOProject are test-only aliases for the
// unexported wire-shape structs in `azuredevops.go`. External tests construct
// payloads with these aliases to drive `isSkinnyADOResource` and
// `mergeHydratedADOResource` without poking at internal names.
type (
	ADOResource   = adoResource
	ADORepository = adoRepository
	ADOProject    = adoProject
)

// MergeHydratedADOResource exposes the merge helper for tests.
func MergeHydratedADOResource(original, hydrated ADOResource) ADOResource {
	return mergeHydratedADOResource(original, hydrated)
}

// WebhookDedupCache is the test-only alias for the unexported
// `webhookDedupCache` so external tests can drive the cache with a
// frozen clock instead of `time.Now()` (deterministic, no flakes).
type WebhookDedupCache = webhookDedupCache

// NewWebhookDedupCache constructs a cache with the supplied TTL. The
// production path uses the package-level `webhookDedupTTL` constant;
// tests use shorter TTLs so a single test run can exercise the
// expiry branch without a real-time wait.
func NewWebhookDedupCache(ttl time.Duration) *WebhookDedupCache {
	return newWebhookDedupCache(ttl)
}

// SeenRecently exposes the unexported `seenRecently` method on the
// cache so external tests can drive both the "first call records"
// and "subsequent within TTL returns true" branches.
func (c *WebhookDedupCache) SeenRecently(key string, now time.Time) bool {
	return c.seenRecently(key, now)
}

// Forget exposes the unexported `forget` method on the cache so
// external tests can verify the rollback contract — when a caller
// records a key but the work it intended to gate (typically
// `submitter.Submit`) fails, calling Forget puts the cache back to
// the state that lets a retry through.
func (c *WebhookDedupCache) Forget(key string) { c.forget(key) }

// Renewal-loop invariant constants re-exported so the
// `TestLeaseDurationAndRenewIntervalInvariant` row in
// `dedup_lease_test.go` can pin the relationship without forcing the
// production constants to be exported. A future refactor that drops
// the freshness window below `renew interval + API timeout` would
// silently regress the dedup correctness — the invariant test fails
// if that ever happens.
var (
	LeaseDurationSecondsForTest = leaseDurationSeconds
	LeaseRenewIntervalForTest   = leaseRenewInterval
	LeaseAPITimeoutForTest      = leaseAPITimeout
)

// DedupRenewIntervalForTest re-exports the dispatcher-level renewal
// cadence so tests can assert the loop's tick frequency without
// timing-sensitive sleeps.
var DedupRenewIntervalForTest = dedupRenewInterval

// API-server error constructors re-exported so the lease fake can
// return the exact error shapes the production code branches on, without
// exporting the status-error type itself. The reason strings are the
// ones the Kubernetes API server sets on these outcomes; `isNotFound`
// and `isAlreadyExists` read them.
func NewNotFoundErrorForTest() error {
	return &apiStatusError{Verb: "GET", Code: http.StatusNotFound, Reason: reasonNotFound}
}

func NewAlreadyExistsErrorForTest() error {
	return &apiStatusError{Verb: "POST", Code: http.StatusConflict, Reason: reasonAlreadyExist}
}

// NewConflictErrorForTest is the *other* 409 — a failed precondition, not
// a name collision. Distinguishing the two is exactly why the production
// code reads `reason` instead of the status code.
func NewConflictErrorForTest() error {
	return &apiStatusError{Verb: "DELETE", Code: http.StatusConflict, Reason: "Conflict"}
}

// Error predicates re-exported so the adapter tests can pin that a real
// API-server response body maps to the outcome the dedup dance branches
// on. These two decisions are what the whole backend rests upon.
var (
	IsNotFoundForTest         = isNotFound
	IsAlreadyExistsForTest    = isAlreadyExists
	NewLeaseRESTClientForTest = newLeaseRESTClientForTest
	TokenSourceForTest        = tokenSourceForTest
)

// newLeaseRESTClientForTest points the REST adapter at an arbitrary base
// URL (an `httptest` server) with a fixed bearer token, so the wire
// contract can be exercised without a cluster.
func newLeaseRESTClientForTest(baseURL, namespace, token string) LeaseClient {
	source := &tokenSource{
		path:         "",
		refreshAfter: tokenRefreshInterval,
		cached:       token,
		readAt:       time.Now(),
	}
	return newLeaseRESTClient(baseURL, namespace, http.DefaultClient, source)
}

// tokenSourceForTest exposes the ServiceAccount token reader with a zero
// refresh window, so every call re-reads the file. That is what makes the
// rotation and read-failure paths reachable from a test — with the
// production window the cache answers first and the filesystem is never
// touched.
func tokenSourceForTest(path string) func() (string, error) {
	source := &tokenSource{path: path, refreshAfter: 0}
	return source.get
}

// MarkInFlightForTest exposes the unexported `trackInFlight` helper so
// external dispatcher tests can populate the in-flight set without
// driving the full webhook handler stack. The production code only
// calls `trackInFlight` from `dedupSeen` (after `SeenRecently` returns
// false); the test analogue avoids the SeenRecently call so the test
// can assert the populated `ReleaseAllInFlight` path in isolation.
func (d *Dispatcher) MarkInFlightForTest(key string) { d.trackInFlight(key) }
