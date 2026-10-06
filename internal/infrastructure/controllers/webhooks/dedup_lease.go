// Package webhooks — see `dedup_cache.go` for the in-memory backend
// and the WebhookDedup contract. This file holds the cross-pod
// distributed-lock backend backed by Kubernetes `coordination.k8s.io/v1`
// `Lease` objects.
//
// # Why a Kubernetes Lease
//
// The bot runs on AKS with `replicas: 2`. Azure DevOps fires both
// `git.pullrequest.created` AND `git.pullrequest.updated` for every
// new PR; the K8s `Service` round-robins one delivery to each pod so
// each pod thinks it is the first to see the PR, both run the AI,
// both post comments — observed live across `internal-terraform/internal-customer-app#NNNN`,
// `internal-terraform/pipelines#NNNN`, and `internal-terraform/internal-customer-app#NNNN`
// on `2026-05-01`. The in-memory cache from PR `#100` cannot dedup
// across pods (each pod sees only its own deliveries).
//
// Operator-rejected alternatives:
//   - `replicas: 1` — gives up availability during deploys / restarts.
//   - drop one ADO subscription — loses coverage for the events the
//     dropped subscription was the only carrier for.
//
// `Lease` is the same primitive client-go's leader-election library
// uses, and the K8s API server already provides optimistic concurrency
// for it via the `Create` verb: exactly one `Create` for a given
// `(namespace, name)` succeeds; every concurrent `Create` returns
// `AlreadyExists`. That is precisely the semantics we need for
// "exactly one pod processes this PR".
//
// Kubernetes does NOT auto-delete `Lease` objects when
// `spec.leaseDurationSeconds` elapses — that field is metadata that
// clients use to decide whether the lease is stale. Self-healing
// across crashes therefore requires two explicit pieces of work that
// this package implements:
//
//  1. The owning pod calls `Forget` (which `Delete`s the lease) AFTER
//     the worker finishes, so a real follow-up push minutes later
//     re-acquires immediately. The serve controller's pool handler
//     `defer`s this so success and failure paths both release.
//  2. When `Create` returns `AlreadyExists`, `SeenRecently` does a
//     stale-lease takeover: it `Get`s the holding lease, checks whether
//     `acquireTime + leaseDurationSeconds` (or `renewTime + duration`,
//     whichever is later) has already passed, and if so `Delete`s the
//     stale lease (with a UID precondition for race safety) and retries
//     `Create`. This is what makes a pod crash mid-review recover —
//     the next webhook delivery for the same PR after the duration
//     elapsed re-acquires; the duration is the maximum window during
//     which a crashed lease blocks new work.
//
// # Required RBAC
//
// The bot's ServiceAccount must be granted the following Role +
// RoleBinding in the namespace it runs in. This is intentionally NOT
// shipped in the application repo — it is operator-side configuration
// applied via the deployment manifests / Terraform module.
//
//	apiVersion: rbac.authorization.k8s.io/v1
//	kind: Role
//	metadata:
//	  namespace: code-guru
//	  name: code-guru-webhook-dedup
//	rules:
//	  - apiGroups: ["coordination.k8s.io"]
//	    resources: ["leases"]
//	    verbs: ["get", "list", "create", "delete", "update", "patch"]
//	---
//	apiVersion: rbac.authorization.k8s.io/v1
//	kind: RoleBinding
//	metadata:
//	  namespace: code-guru
//	  name: code-guru-webhook-dedup
//	subjects:
//	  - kind: ServiceAccount
//	    name: code-guru
//	    namespace: code-guru
//	roleRef:
//	  apiGroup: rbac.authorization.k8s.io
//	  kind: Role
//	  name: code-guru-webhook-dedup
//	  apiGroup: rbac.authorization.k8s.io
//
// `update` and `patch` are listed for forward compatibility with a
// future "renew the lease while a long review is in progress" path;
// today the implementation only uses `create` and `delete`.
package webhooks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	logger "github.com/sirupsen/logrus"
)

// leaseAPITimeout caps every call to the K8s API server. The webhook
// hot path MUST NOT stall on a wedged control plane; on timeout the
// dedup degrades to "process the webhook" — never worse than today's
// no-dedup baseline.
const leaseAPITimeout = 5 * time.Second

// leaseDurationSeconds is the freshness window applied to every dedup
// lease. After every `leaseRenewInterval` an in-flight review's
// renewal goroutine `Update`s the lease's `renewTime`, so a healthy
// pod's lease never expires from the takeover path's perspective.
// When the pod dies (kill -9, OOM, SIGTERM-after-drain-timeout) the
// renewal loop dies with it; the lease becomes takeover-eligible
// `leaseDurationSeconds` after the last successful renewal — which
// caps how long an orphaned lease blocks new webhook deliveries for
// the same PR.
//
// `60s` keeps that orphan-recovery window short while still leaving
// generous headroom over `leaseRenewInterval (30s) + leaseAPITimeout
// (5s) + jitter`, so a transient API-server blip during a review
// cannot orphan an in-flight lease. Operationally observed orphan
// windows before this fix landed went up to 12 minutes; with
// `60s + leaseRenewInterval` the worst case drops to ≤60s after a
// crashed pod's last successful renewal.
const leaseDurationSeconds int32 = 60

// leaseRenewInterval is how often an in-flight review's renewal
// goroutine refreshes the lease's `renewTime` via `Get` + `Update`.
// MUST be strictly less than `leaseDurationSeconds - leaseAPITimeout`
// so a single missed renewal (e.g. the API server is slow) does not
// let the lease expire while the pod is still alive. The 2× ratio
// (renew at half the freshness window) follows the standard K8s
// lease-renewal pattern (see `k8s.io/client-go/tools/leaderelection`
// for the same shape) and gives one full retry budget per window.
const leaseRenewInterval = 30 * time.Second

// Compile-time invariant: a successful renewal must always land
// before the freshness window closes, otherwise an in-flight review's
// own lease would be eligible for takeover by the next webhook for
// the same PR. The `leaseAPITimeout` headroom covers a single slow
// `Update` round-trip; jitter beyond that is absorbed by the next
// scheduled tick of the renewal loop.
const _ uint = uint(int64(leaseDurationSeconds)*int64(time.Second)) -
	uint(int64(leaseRenewInterval)+int64(leaseAPITimeout))

// k8sNameMaxLen mirrors RFC 1123 subdomain limits enforced by the K8s
// API server. Lease names that exceed this are rejected with
// `Invalid` — defending in the client keeps the failure mode in our
// own log instead of producing an opaque API-server error.
const k8sNameMaxLen = 253

// Logrus field keys reused across every lease-backend log call. Pulled
// out so a future rename only happens once and the linter's repeated-
// string-literal alarm goes quiet.
const (
	logFieldKey       = "key"
	logFieldLeaseName = "lease_name"
)

// LeaseClient is the narrow port the dedup backend needs from the
// `coordination.k8s.io/v1` API — four verbs on one namespaced resource.
// `dedup_lease_client.go` implements it over `net/http`; tests implement
// it with an in-memory fake.
//
// The signatures carry only what the callers actually decide on, so the
// per-verb option structs the Kubernetes Go client exposes collapse to
// the single precondition this package uses (`Delete`'s UID).
//
// `Get` is required by the stale-lease takeover path in
// `SeenRecently` — when `Create` hits `AlreadyExists` we must inspect
// the holder's `acquireTime` to decide whether to take it over.
type LeaseClient interface {
	Create(ctx context.Context, lease *Lease) (*Lease, error)
	Get(ctx context.Context, name string) (*Lease, error)
	// Delete removes the lease. A non-empty `uid` is sent as a
	// precondition so a takeover cannot remove a lease that another pod
	// recreated between our Get and our Delete.
	Delete(ctx context.Context, name, uid string) error
	// Update is required by the renewal loop. The bot's RBAC `Role`
	// already grants `update` on `coordination.k8s.io/leases` (see the
	// shared-toolbox terraform `kubernetes_role.lease_dedup`), so adding
	// it to the client interface costs no operator action.
	Update(ctx context.Context, lease *Lease) (*Lease, error)
}

// K8sLeaseDedup is the cross-pod WebhookDedup backend. It treats a
// successful `Create` of a `Lease` as the dedup acquisition (this pod
// owns the review), and a `Delete` as the explicit release that lets
// a real follow-up push re-acquire immediately.
type K8sLeaseDedup struct {
	client LeaseClient
	// holderIdentity is recorded on every lease so logs / `kubectl get
	// leases` show which pod owns a given review. The K8s downward API
	// usually populates `HOSTNAME` with the pod name; tests inject a
	// fixed value for determinism.
	holderIdentity string
}

// NewK8sLeaseDedup wires the lease-backed dedup. `client` is typically
// `kubernetes.NewForConfig(cfg).CoordinationV1().Leases(namespace)`;
// tests pass a hand-rolled fake.
func NewK8sLeaseDedup(client LeaseClient, holderIdentity string) *K8sLeaseDedup {
	return &K8sLeaseDedup{client: client, holderIdentity: holderIdentity}
}

// NewK8sLeaseDedupFromInCluster wires the lease-backed dedup using
// the in-cluster Kubernetes config (the ServiceAccount token + CA
// mounted into every pod). Returns `ErrLeaseClientNotConfigured`
// when the process is not running inside a pod, so the caller can
// fall back to the in-memory backend without surfacing the error to
// the operator (the fallback is a valid local-dev workflow).
//
// `namespace` is the namespace the leases will be created in. When
// empty, the function reads the standard
// `/var/run/secrets/kubernetes.io/serviceaccount/namespace` file —
// the same default `client-go` uses for namespace-scoped operations.
//
// `holderIdentity` is recorded on every lease so `kubectl get
// leases` shows which pod owns a given review. Production callers
// pass `os.Hostname()` (the pod name when the downward API populates
// it); a constant per-pod value works fine for local testing.
func NewK8sLeaseDedupFromInCluster(namespace, holderIdentity string) (*K8sLeaseDedup, error) {
	if !IsInKubernetes() {
		return nil, ErrLeaseClientNotConfigured
	}
	if namespace == "" {
		ns, readErr := os.ReadFile(podNamespaceFile)
		if readErr != nil {
			return nil, fmt.Errorf("dedup-lease: read pod namespace from %s: %w", podNamespaceFile, readErr)
		}
		namespace = strings.TrimSpace(string(ns))
	}
	if namespace == "" {
		return nil, errors.New("dedup-lease: pod namespace is empty after fallback read")
	}
	client, err := newInClusterLeaseClient(namespace)
	if err != nil {
		return nil, err
	}
	return NewK8sLeaseDedup(client, holderIdentity), nil
}

// SeenRecently performs the optimistic-create dance against the K8s
// API server with a stale-lease takeover fallback:
//
//   - `Create` returns 201 Created → this pod acquired the lease, the
//     caller proceeds.
//   - `Create` returns 409 AlreadyExists → `Get` the holding lease and
//     check whether `acquireTime + leaseDurationSeconds` (or the more
//     recent `renewTime`) has already passed. If yes → `Delete` it
//     under a UID precondition (atomic against a concurrent renewer)
//     and retry `Create`; the retry's outcome is the answer. If no →
//     the holder is alive, return "duplicate".
//   - Any other error → degrade to "not seen" so the webhook is
//     processed (the same baseline as no dedup at all).
//
// Without the takeover branch, a pod that crashes mid-review would
// leak its lease in etcd forever and block all future deliveries for
// the same PR — Kubernetes does NOT auto-delete `Lease` objects when
// `leaseDurationSeconds` elapses.
func (d *K8sLeaseDedup) SeenRecently(ctx context.Context, key string) bool {
	leaseName := sanitizeLeaseName(key)

	callCtx, cancel := context.WithTimeout(ctx, leaseAPITimeout)
	defer cancel()

	if _, err := d.client.Create(callCtx, d.buildLease(leaseName)); err == nil {
		return false
	} else if !isAlreadyExists(err) {
		logger.WithFields(deliveryFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
		})).Warnf("dedup-lease: Create failed (%v) — falling back to process the webhook", err)
		return false
	}

	// `AlreadyExists` — inspect the holder to decide between "live
	// lease, real duplicate" and "stale lease from a crashed pod".
	existing, err := d.client.Get(callCtx, leaseName)
	if isNotFound(err) {
		// Race: the holder Delete'd between our Create and Get. Retry
		// Create — most of the time this succeeds because the slot is
		// free; if a third pod beat us to it the second Create returns
		// AlreadyExists and we treat the delivery as a duplicate.
		if _, retryErr := d.client.Create(callCtx, d.buildLease(leaseName)); retryErr == nil {
			return false
		}
		return true
	}
	if err != nil {
		logger.WithFields(deliveryFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
		})).Warnf("dedup-lease: Get after AlreadyExists failed (%v) — treating as duplicate to be safe", err)
		return true
	}

	if !leaseExpired(existing, time.Now()) {
		// Holder is alive — this is a real cross-pod duplicate.
		return true
	}

	// Stale lease — take it over with a UID precondition so a renewer
	// that just refreshed it cannot lose its work to our cleanup.
	if delErr := d.client.Delete(callCtx, leaseName, existing.Metadata.UID); delErr != nil && !isNotFound(delErr) {
		logger.WithFields(deliveryFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
			"holder":          stringValue(existing.Spec.HolderIdentity),
		})).Warnf("dedup-lease: takeover Delete failed (%v) — treating as duplicate; the next delivery will retry", delErr)
		return true
	}
	if _, retryErr := d.client.Create(callCtx, d.buildLease(leaseName)); retryErr == nil {
		logger.WithFields(deliveryFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
			"prev_holder":     stringValue(existing.Spec.HolderIdentity),
		})).Info("dedup-lease: took over a stale lease (previous holder likely crashed mid-review)")
		return false
	}
	// Another pod won the takeover race — they are the new owner.
	return true
}

// buildLease constructs the canonical `Lease` payload this pod tries
// to `Create`. Pulled out so the takeover-retry path uses the exact
// same shape (with a fresh `acquireTime`) as the first attempt.
func (d *K8sLeaseDedup) buildLease(name string) *Lease {
	duration := leaseDurationSeconds
	now := MicroTime{Time: time.Now()}
	holder := d.holderIdentity
	return &Lease{
		APIVersion: leaseAPIVersion,
		Kind:       leaseKind,
		Metadata:   LeaseMeta{Name: name},
		Spec: LeaseSpec{
			HolderIdentity:       &holder,
			LeaseDurationSeconds: &duration,
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}
}

// leaseExpired returns true when the held lease's freshness window
// (the more recent of `renewTime` and `acquireTime`, plus
// `leaseDurationSeconds`) is already in the past relative to `now`.
// A lease with no times set or no duration is treated as expired so
// the takeover path can recover from a malformed object — that
// shape is "shouldn't happen" but the safe behaviour is to let
// recovery proceed rather than block forever.
func leaseExpired(lease *Lease, now time.Time) bool {
	if lease == nil || lease.Spec.LeaseDurationSeconds == nil {
		return true
	}
	var t time.Time
	if lease.Spec.RenewTime != nil {
		t = lease.Spec.RenewTime.Time
	}
	if lease.Spec.AcquireTime != nil && lease.Spec.AcquireTime.Time.After(t) {
		t = lease.Spec.AcquireTime.Time
	}
	if t.IsZero() {
		return true
	}
	expiresAt := t.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second)
	return !now.Before(expiresAt)
}

func stringValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// Forget releases the lease so a legitimate re-review (e.g. a new
// push minutes later) can re-acquire immediately without waiting for
// the TTL. Idempotent: a `NotFound` is treated as success because the
// caller may invoke `Forget` after the lease has already aged out, or
// after another rollback path has already removed it.
func (d *K8sLeaseDedup) Forget(ctx context.Context, key string) {
	leaseName := sanitizeLeaseName(key)
	callCtx, cancel := context.WithTimeout(ctx, leaseAPITimeout)
	defer cancel()

	err := d.client.Delete(callCtx, leaseName, "")
	if err == nil || isNotFound(err) {
		return
	}
	logger.WithFields(deliveryFields(logger.Fields{
		logFieldKey:       key,
		logFieldLeaseName: leaseName,
	})).Warnf("dedup-lease: Delete failed (%v) — lease will block new deliveries until the next caller's takeover path runs Get + UID-conditioned Delete", err)
}

// Renew refreshes the held lease's `renewTime` so a long-running review
// holds its dedup slot beyond a single `leaseDurationSeconds` window.
// The serve controller's worker handler kicks off a goroutine that calls
// Renew on a `leaseRenewInterval` ticker for every in-flight job; that
// goroutine exits when the job's context is cancelled (which the
// `defer ReleaseDedup` also triggers). The ratio of `renew interval :
// freshness window = 1:2` follows the standard K8s leader-election
// pattern (`k8s.io/client-go/tools/leaderelection`) and gives the
// renewer one full retry budget per window.
//
// Renew is best-effort. The contract is "renewal failures MUST NOT panic
// or kill the loop" — a transient API-server blip must not orphan an
// in-flight review's lease. Failures log at warn so an operator can
// correlate; a `NotFound` is logged at debug because it is the expected
// shape when a takeover from another pod has just stolen the lease (in
// which case the next webhook for this PR will go to the new holder, not
// us). On every other error path we keep the loop running — the next
// tick may succeed and we still hold the slot until then.
func (d *K8sLeaseDedup) Renew(ctx context.Context, key string) {
	leaseName := sanitizeLeaseName(key)
	callCtx, cancel := context.WithTimeout(ctx, leaseAPITimeout)
	defer cancel()

	// `Get` first so the `Update` carries the current `ResourceVersion`
	// (mandatory for conflict-detection on K8s updates) and the existing
	// `holderIdentity` / `acquireTime` round-trip unchanged. A separate
	// `Patch` verb would avoid the round-trip but the `LeaseClient`
	// interface kept the surface small — `Get` + `Update` is well under
	// one second of latency in practice and matches the shape
	// `SeenRecently` already uses for the takeover path.
	existing, err := d.client.Get(callCtx, leaseName)
	if isNotFound(err) {
		logger.WithFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
		}).Debugf("dedup-lease: Renew Get returned NotFound — lease was likely released or stolen; the renewal loop will keep trying until the worker cancels it")
		return
	}
	if err != nil {
		d.logRenewError(key, leaseName, "Get", err)
		return
	}

	now := MicroTime{Time: time.Now()}
	existing.Spec.RenewTime = &now

	if _, updateErr := d.client.Update(callCtx, existing); updateErr != nil {
		// Update racing with Forget / takeover Delete returns NotFound;
		// that is the expected shape, not a fault. Drop to debug so
		// shutdown / takeover scenarios stay quiet in operator logs.
		if isNotFound(updateErr) {
			logger.WithFields(logger.Fields{
				logFieldKey:       key,
				logFieldLeaseName: leaseName,
			}).Debugf("dedup-lease: Renew Update returned NotFound — lease was concurrently released or stolen; nothing to refresh")
			return
		}
		d.logRenewError(key, leaseName, "Update", updateErr)
	}
}

// logRenewError centralises the warn-vs-debug decision for transient
// renewal failures so the Get and Update branches stay in sync. Errors
// caused by the loop's own context being cancelled (the documented
// shutdown path: `defer cancelRenew()` on job completion, or the
// `context.WithTimeout` running out under a wedged API server) are
// logged at debug because they are exactly what the contract expects;
// every other error logs at warn so an operator scanning logs sees the
// API-server blips that warrant investigation.
func (d *K8sLeaseDedup) logRenewError(key, leaseName, op string, err error) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		logger.WithFields(logger.Fields{
			logFieldKey:       key,
			logFieldLeaseName: leaseName,
		}).Debugf("dedup-lease: Renew %s aborted by ctx (%v) — expected during shutdown / job completion", op, err)
		return
	}
	logger.WithFields(logger.Fields{
		logFieldKey:       key,
		logFieldLeaseName: leaseName,
	}).Warnf("dedup-lease: Renew %s failed (%v) — keeping the loop alive; the next tick may succeed", op, err)
}

// sanitizeLeaseName converts an arbitrary dedup key (e.g.
// `ado:repo-id:12345`) into a string that conforms to the RFC 1123
// subdomain rules the K8s API server enforces on resource names:
// lowercase alphanumeric or `-`, ≤ 253 chars, must start and end with
// alphanumeric. The transformation is deterministic so two callers
// producing the same key always land on the same lease name.
//
// To prevent distinct keys from colliding under the lossy `[^a-z0-9-]
// -> -` mapping (e.g. GitHub owners that legitimately contain `-`
// versus `/` boundary replacements), the name embeds a deterministic
// SHA-256 prefix of the original key as a suffix. The hash makes
// "different keys → different names" a hard guarantee while the
// readable prefix keeps `kubectl get leases` informative.
//
// The `code-guru-` prefix scopes the lease names to this application
// so a `kubectl get leases` against a shared namespace shows our
// dedup leases distinctly from leader-election leases owned by other
// controllers.
func sanitizeLeaseName(key string) string {
	const (
		prefix     = "code-guru-"
		hashHexLen = 16 // 64 bits — collision-resistant for our key cardinality
	)

	hash := sha256.Sum256([]byte(key))
	hashHex := hex.EncodeToString(hash[:])[:hashHexLen]

	var b strings.Builder
	b.Grow(len(prefix) + len(key) + 1 + hashHexLen)
	b.WriteString(prefix)
	for _, r := range strings.ToLower(key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			// `:` from `ado:repo:pr` and `/` from `gh:owner/repo:pr`
			// are the two characters we hit in practice; `-` keeps the
			// segment boundaries readable in `kubectl get leases`.
			b.WriteRune('-')
		}
	}
	readable := b.String()

	// Reserve room for the hash + separator so the final name fits
	// `k8sNameMaxLen`. The readable portion is truncated rather than
	// the hash because the hash carries the uniqueness guarantee.
	maxReadable := k8sNameMaxLen - 1 - hashHexLen
	if len(readable) > maxReadable {
		readable = readable[:maxReadable]
	}
	// Trim trailing `-` from the readable portion so the boundary
	// before the hash is alphanumeric — keeps the name visually
	// consistent and avoids `--` runs from truncation.
	readable = strings.TrimRight(readable, "-")
	if readable == "" || readable == strings.TrimRight(prefix, "-") {
		// Pathological all-symbols key (not reachable from production
		// shapes). Fall back to the prefix + hash so the name is still
		// unique and RFC-1123 valid.
		return prefix + hashHex
	}
	return readable + "-" + hashHex
}

// ErrLeaseClientNotConfigured is returned by NewK8sLeaseDedupFromInCluster
// when the caller asks for an in-cluster client outside of a pod (no
// `KUBERNETES_SERVICE_HOST`). The caller is expected to fall back to
// the in-memory backend in that case.
var ErrLeaseClientNotConfigured = errors.New("K8s in-cluster config not available; falling back to in-memory dedup")

// kubernetesServiceHostEnv is the env var the kubelet injects into
// every pod. Its presence is the canonical signal "I am running
// inside a Kubernetes pod" — the same check the standard
// `rest.InClusterConfig` uses internally to decide whether the
// in-cluster wiring is available.
const kubernetesServiceHostEnv = "KUBERNETES_SERVICE_HOST"

// podNamespaceFile is the standard projected path for the pod's
// namespace. Every pod has it mounted by the kubelet via the
// downward API, so we never need to require an explicit
// `POD_NAMESPACE` env var on the deployment manifest.
const podNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

// IsInKubernetes returns true when the process is running inside a
// Kubernetes pod. Used by the serve controller to decide between the
// cross-pod K8s-Lease dedup backend and the per-pod in-memory cache.
// Lives next to the lease implementation so the heuristic is in one
// place — when the heuristic needs to evolve (e.g. adding a forced
// `CODE_GURU_DEDUP_BACKEND=lease` override), it changes here.
func IsInKubernetes() bool {
	return os.Getenv(kubernetesServiceHostEnv) != ""
}
