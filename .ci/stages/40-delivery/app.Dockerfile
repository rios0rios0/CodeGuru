# syntax=docker/dockerfile:1.7

# The builder tag must track the `go` directive in `go.mod` and be bumped in
# the same commit. The official golang images set `GOTOOLCHAIN=local`, so a
# builder older than that directive does not quietly download a newer
# toolchain -- it fails the build outright with
# `go.mod requires go >= X (running go Y; GOTOOLCHAIN=local)`.
FROM golang:1.27.2-alpine AS builder
WORKDIR /src

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/code-guru \
    ./cmd/code-guru

# Pinned to the digest of the `debian:13-slim` tag at build time so layer
# resolution is reproducible and unaffected by mutable-tag drift. Bump the
# digest deliberately when refreshing the base; the `:13-slim` tag itself
# is left in the reference for human readability.
FROM debian:13-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132

# Use bash for RUN steps with `pipefail` enabled at the shell level so any
# pipe (e.g. `claude --version | tee /etc/claude-version` below) propagates
# failures from the upstream command. Setting `-o pipefail` on SHELL itself
# also satisfies hadolint DL4006 — the inline `set -euxo pipefail` further
# down is now redundant defense-in-depth.
SHELL ["/bin/bash", "-eo", "pipefail", "-c"]

# Install the Claude Code native binary to a system path so it survives the
# Kubernetes emptyDir mount on /home/nonroot/.claude (which masks anything the
# image puts under that subtree). The official installer drops the binary into
# `$HOME/.local/bin`, so we install with HOME=/opt/claude-install and then
# move the result onto PATH. `curl` is removed at the end of the same RUN to
# keep it out of the final image layer.
#
# The installer is invoked with the explicit `stable` channel argument so the
# image is insulated from a future change to the installer's default channel,
# while still avoiding a hard version pin (security fixes ship without a
# manual bump).
#
# The installer is downloaded to a file and then executed, rather than piped
# into bash, so a `curl` failure surfaces as the failing command instead of
# being masked. `set -euxo pipefail` is added as defense in depth in case
# future edits reintroduce a pipe.
#
# `https://claude.ai/install.sh` answers with a redirect to the release CDN
# (`downloads.claude.ai`), so `-L` is required. `--proto`/`--proto-redir`
# pin the initial request AND every redirect to HTTPS: without them a
# redirect could downgrade the download to plain HTTP and an on-path
# attacker could swap the installer (Sonar S6506). No `--max-redirs` cap on
# purpose -- the hop count is the installer's to change, and a second HTTPS
# hop is harmless.
#
# The resolved version is written to /etc/claude-version (and emitted in the
# build log) so operators can correlate runtime behavior with the exact CLI
# version installed at build time -- otherwise the floating channel makes
# version-specific debugging significantly harder.
RUN set -euxo pipefail; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl libstdc++6; \
    groupadd --system --gid 65532 nonroot; \
    useradd --system --gid nonroot --uid 65532 --create-home \
        --home-dir /home/nonroot --shell /sbin/nologin nonroot; \
    mkdir -p /opt/claude-install; \
    curl -fsSL --proto "=https" --proto-redir "=https" \
        https://claude.ai/install.sh -o /tmp/claude-install.sh; \
    HOME=/opt/claude-install bash /tmp/claude-install.sh stable; \
    install -m 0755 /opt/claude-install/.local/bin/claude /usr/local/bin/claude; \
    /usr/local/bin/claude --version | tee /etc/claude-version; \
    rm -rf /opt/claude-install /tmp/claude-install.sh; \
    apt-get purge -y --auto-remove curl; \
    apt-get clean; \
    rm -rf /var/lib/apt/lists/*

# HOME drives where claude looks for `~/.claude/` (token cache, sessions,
# settings) and `~/.local/share/claude/` (version metadata). Keep the value
# stable across the image and the K8s manifest.
ENV HOME=/home/nonroot

COPY --from=builder /out/code-guru /usr/local/bin/code-guru

USER 65532:65532
WORKDIR /home/nonroot

EXPOSE 8080

# Probe the local /health endpoint via the Go binary itself.
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/code-guru", "health", "--timeout=4s"]

ENTRYPOINT ["/usr/local/bin/code-guru"]
CMD ["serve"]
