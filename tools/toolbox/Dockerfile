# syntax=docker/dockerfile:1.7
# Xalgorix toolbox image — the per-project execution plane.
#
# Build from the repository root (the Go stage needs go.mod):
#   docker build -f tools/toolbox/Dockerfile --target core -t xalgorix-toolbox:dev .
#
# Profiles:
#   core  — recon, HTTP/fuzzing, SAST, runtime toolchains (no privileged tools)
#   full  — core + raw-socket/network/MITM/exploitation/credential tooling
#   web   — core + headless Chromium (DevTools port published for the platform)
#
# The image contains NO secrets and no scan state. It is disposable: the platform
# mounts the scan directory at the same absolute path and tears the container down
# when the scan ends.

# ── Stage 1: build the in-container helpers (static, pure Go) ────────────────
FROM golang:1.26-bookworm AS gobuild
# Same GOPROXY build argument as the platform image: a mirror can be selected
# on networks where the default module proxy is unreachable.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/toolshim ./cmd/toolshim
COPY tools/toolbox/selftest ./tools/toolbox/selftest
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags "-s -w" -o /out/toolshim ./cmd/toolshim \
 && go build -trimpath -ldflags "-s -w" -o /out/toolbox-selftest ./tools/toolbox/selftest

# ── Stage 2: base runtime ────────────────────────────────────────────────────
FROM kalilinux/kali-rolling AS base
ARG TARGETARCH
ARG KALI_MIRROR=http://mirrors.ustc.edu.cn/kali
# Same module-proxy knob as the builder stage; install.sh runs `go install`
# here for the core/net/sast tool sets.
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
ENV DEBIAN_FRONTEND=noninteractive

# Pin Kali's own CDN mirror instead of the geo redirector (a redirector outage
# otherwise fails every build).
RUN set -eux; \
    for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.sources /etc/apt/sources.list.d/*.list; do \
      [ -f "$f" ] || continue; \
      sed -i -E "s#https?://http\.kali\.org/kali/?#${KALI_MIRROR}#g" "$f"; \
    done; \
    ! grep -rqs 'http\.kali\.org' /etc/apt/sources.list /etc/apt/sources.list.d/ 2>/dev/null || true

COPY tools/toolbox/versions.lock /opt/xalgorix/versions.lock
COPY tools/toolbox/install.sh /opt/xalgorix/install.sh
RUN chmod +x /opt/xalgorix/install.sh && /opt/xalgorix/install.sh base

# Go 1.26 from the builder: Kali's golang-go lags behind the `go >= 1.26`
# requirement of the ProjectDiscovery tools and makes `go install ...@latest`
# fail silently. This also matches the toolpack's /usr/local/go/bin/go path.
COPY --from=gobuild /usr/local/go /usr/local/go

# Publish the repo-built helpers on a PATH directory that precedes distro bins.
COPY --from=gobuild /out/toolshim /opt/xalgorix/bin/toolshim
COPY --from=gobuild /out/toolbox-selftest /opt/xalgorix/bin/toolbox-selftest
COPY tools/toolbox/toolpack.yaml /opt/xalgorix/toolpack.yaml

# File capabilities are stripped AFTER each profile's package installs: apt
# re-adds setcap capabilities when nmap/masscan/arping are installed again in
# the net/exploit layers, and a binary requesting an effective cap outside the
# container's bounding set fails execve with EPERM.

ENV PATH="/opt/xalgorix/bin:/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/root/go/bin:/root/.local/bin:/root/.cargo/bin" \
    HOME=/root \
    LANG=C.UTF-8 \
    LC_ALL=C.UTF-8 \
    XALGORIX_PROJECT_CONTAINER=1 \
    DEBIAN_FRONTEND=noninteractive

WORKDIR /
# The platform overrides Cmd with `sleep infinity`; this default keeps the image
# runnable standalone for debugging.
CMD ["sleep", "infinity"]

# ── Stage 3: core profile ────────────────────────────────────────────────────
FROM base AS core
RUN /opt/xalgorix/install.sh core \
 && getcap -r / 2>/dev/null | awk '{print $1}' | while read -r f; do setcap -r "$f" 2>/dev/null || true; done || true

# ── Stage 4: full profile (raw sockets, exploitation, credentials) ───────────
FROM core AS full
RUN /opt/xalgorix/install.sh net && /opt/xalgorix/install.sh exploit && /opt/xalgorix/install.sh sast \
 && getcap -r / 2>/dev/null | awk '{print $1}' | while read -r f; do setcap -r "$f" 2>/dev/null || true; done || true

# ── Stage 5: web profile (headless Chromium) ─────────────────────────────────
FROM core AS web
RUN /opt/xalgorix/install.sh browser \
 && getcap -r / 2>/dev/null | awk '{print $1}' | while read -r f; do setcap -r "$f" 2>/dev/null || true; done || true

# ── Stage 6: self-test gate ──────────────────────────────────────────────────
# CI builds this target and treats a non-zero exit as a failed image. The
# platform's PrefetchImage/Selftest jobs run the same binary in a live container.
FROM core AS verify
ARG PROFILE=core
RUN /opt/xalgorix/bin/toolbox-selftest --profile ${PROFILE} --json
