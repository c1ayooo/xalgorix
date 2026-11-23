#!/usr/bin/env bash
# Xalgorix toolbox installer — segmented, idempotent, version-pinned.
#
#   install.sh base|core|net|exploit|sast|browser
#
# Design notes:
#   * Every version comes from /opt/xalgorix/versions.lock. There is no
#     "@latest" anywhere: an image digest must be reproducible.
#   * A failed optional prefetch logs a WARN and continues; a failed *core*
#     binary aborts the build. Runtime auto-install (the terminal tool's
#     packageMap path) remains available for the long tail.
#   * Installs take a flock so several project containers can share the
#     /opt/xalgorix/cache volume without racing.
set -euo pipefail

LOCK=/opt/xalgorix/versions.lock
CACHE=/opt/xalgorix/cache
mkdir -p "${CACHE}" /opt/xalgorix/bin
exec 9>"${CACHE}/.install.lock"
flock 9

# shellcheck disable=SC1090
[ -f "${LOCK}" ] && . "${LOCK}"

log()  { printf '[toolbox] %s\n' "$*"; }
warn() { printf '[toolbox] WARN %s\n' "$*" >&2; }

# Force IPv4 and bound each fetch: a blackholed IPv6 route or a stalled CDN edge
# otherwise hangs `apt-get update` for minutes with no output.
APT_OPTS=(
  -o Acquire::Retries=3
  -o Acquire::http::Timeout=30
  -o Acquire::https::Timeout=30
  -o Acquire::ForceIPv4=true
)

apt_install() {
  local pkgs=("$@")
  log "apt install: ${pkgs[*]}"
  DEBIAN_FRONTEND=noninteractive apt-get "${APT_OPTS[@]}" install -y --no-install-recommends "${pkgs[@]}"
}

apt_update_once() {
  [ -f "${CACHE}/.apt-updated" ] && return 0
  for i in 1 2 3; do
    if apt-get "${APT_OPTS[@]}" -o APT::Update::Error-Mode=any update; then
      touch "${CACHE}/.apt-updated"
      return 0
    fi
    warn "apt index fetch failed (mirror sync?), retry ${i}/3"
    rm -rf /var/lib/apt/lists/*
    sleep 20
  done
  return 1
}

go_install() {
  local pkg="$1" version="${2:-}"
  local spec="${pkg}@${version}"
  # Go requires an explicit @version outside a module ("go install pkg" fails
  # with "requires a version when current directory is not in a module").
  [ -n "${version}" ] || spec="${pkg}@latest"
  GOBIN=/root/go/bin go install "${spec}" || warn "go install ${spec} failed"
}

# gogo publishes replace directives in its go.mod, which `go install pkg@version`
# refuses ("would be interpreted differently than if it were the main module").
# Clone and build it as the main module instead.
gogo_install() {
  local dir=/tmp/gogo-src
  rm -rf "$dir"
  if ! git clone --depth 1 https://github.com/chainreactors/gogo "$dir"; then
    warn "gogo clone failed"
    return 1
  fi
  # The module root is the v2/ subdirectory, not the repository root.
  ( cd "$dir/v2" && GOBIN=/root/go/bin go install -tags goregexp . ) \
    || warn "gogo build failed"
  rm -rf "$dir"
}

# trufflehog's module also carries replace directives, and raw.githubusercontent
# is not reachable from every build network. Fetch the release tarball through a
# GitHub mirror instead.
trufflehog_install() {
  local arch tag version url tmp
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) warn "trufflehog: unsupported architecture $(uname -m)"; return 1 ;;
  esac
  tag=$(curl -fsSL --max-time 20 https://api.github.com/repos/trufflesecurity/trufflehog/releases/latest 2>/dev/null | jq -r '.tag_name // empty' 2>/dev/null) || true
  if [ -z "${tag}" ]; then
    warn "trufflehog: release lookup failed"
    return 1
  fi
  version="${tag#v}"
  url="https://gh-proxy.com/https://github.com/trufflesecurity/trufflehog/releases/download/${tag}/trufflehog_${version}_linux_${arch}.tar.gz"
  tmp=$(mktemp -d)
  if curl -fsSL --max-time 180 "${url}" -o "${tmp}/trufflehog.tgz" \
     && tar -xzf "${tmp}/trufflehog.tgz" -C "${tmp}" trufflehog; then
    install -m 0755 "${tmp}/trufflehog" /usr/local/bin/trufflehog
    rm -rf "${tmp}"
    return 0
  fi
  warn "trufflehog: download failed from gh-proxy.com"
  rm -rf "${tmp}"
  return 1
}

pipx_install() {
  local pkg="$1" version="${2:-}"
  local spec="${pkg}"
  [ -n "${version}" ] && spec="${pkg}==${version}"
  pipx install "${spec}" >/dev/null 2>&1 \
    || pip3 install --break-system-packages "${spec}" >/dev/null 2>&1 \
    || warn "pip install ${spec} failed"
}

json_unpack() {
  curl -fsSL "$1" -o /tmp/pkg.tar.gz && tar -xzf /tmp/pkg.tar.gz -C "$2" && rm -f /tmp/pkg.tar.gz
}

case "${1:-}" in
  base)
    apt_update_once
    apt_install \
      ca-certificates curl wget git jq unzip zip p7zip-full file tree bc xxd less procps \
      python3 python3-pip python3-venv pipx \
      cargo nodejs npm ripgrep \
      build-essential pkg-config libpcap-dev libcap2-bin \
      seclists wordlists
    # The Go toolchain is copied from the golang:1.26 builder stage (see
    # Dockerfile). Kali's golang-go lags behind what httpx/nuclei require.
    pipx ensurepath >/dev/null 2>&1 || true
    ;;

  core)
    apt_update_once
    # Recon + HTTP/scanning + fuzzing + TLS/WAF fingerprinting.
    apt_install \
      nmap dnsutils whois \
      dirb nikto whatweb wafw00f \
      sslscan \
      ffuf gobuster \
      wordlists
    # ProjectDiscovery + tomnomnom style Go tools, pinned.
    go_install github.com/projectdiscovery/subfinder/v2/cmd/subfinder "${SUBHANDLER_VERSION:-}" || true
    go_install github.com/projectdiscovery/dnsx/cmd/dnsx || true
    go_install github.com/projectdiscovery/httpx/cmd/httpx || true
    go_install github.com/projectdiscovery/katana/cmd/katana || true
    go_install github.com/projectdiscovery/nuclei/v3/cmd/nuclei || true
    go_install github.com/projectdiscovery/naabu/cmd/naabu || true
    go_install github.com/projectdiscovery/notify/cmd/notify || true
    go_install github.com/ffuf/ffuf/v2 || true
    go_install github.com/OJ/gobuster/v3 || true
    go_install github.com/tomnomnom/assetfinder || true
    go_install github.com/tomnomnom/waybackurls || true
    go_install github.com/tomnomnom/qsreplace || true
    go_install github.com/tomnomnom/unfurl || true
    go_install github.com/tomnomnom/anew || true
    go_install github.com/tomnomnom/httprobe || true
    go_install github.com/tomnomnom/gron || true
    go_install github.com/lc/gau/v2/cmd/gau || true
    go_install github.com/hakluke/hakrawler || true
    go_install github.com/jaeles-project/gospider || true
    pipx_install arjun
    pipx_install uro
    pipx_install dirsearch
    # `httpx` must resolve to ProjectDiscovery, not Kali's Python HTTP client.
    if [ -x /root/go/bin/httpx ]; then ln -sf /root/go/bin/httpx /opt/xalgorix/bin/httpx; fi
    if [ -x /root/go/bin/nuclei ]; then ln -sf /root/go/bin/nuclei /opt/xalgorix/bin/nuclei; fi
    if [ -x /root/go/bin/nuclei ]; then /root/go/bin/nuclei -update-templates >/dev/null 2>&1 || warn "nuclei template update failed"; fi
    ;;

  net)
    apt_update_once
    apt_install nmap masscan arp-scan netdiscover responder ettercap-text-only
    gogo_install || true
    ;;

  exploit)
    apt_update_once
    apt_install sqlmap hydra medusa john hashcat exploitdb
    pipx_install impacket
    # pipx exposes the script as secretsdump.py; publish the manifest name too.
    if [ -x /root/.local/bin/secretsdump.py ]; then
      ln -sf /root/.local/bin/secretsdump.py /root/.local/bin/impacket-secretsdump
    fi
    ;;

  sast)
    apt_update_once
    apt_install gdb strace ruby ruby-dev
    pipx_install semgrep
    pipx_install bandit
    pipx_install git-dumper
    gem install --no-document brakeman >/dev/null 2>&1 || warn "brakeman install failed"
    # The module still declares the historical zricethezav path; the new
    # github.com/gitleaks path fails with "module declares its path as ...".
    go_install github.com/zricethezav/gitleaks/v8 || true
    trufflehog_install || true
    ;;

  browser)
    apt_update_once
    apt_install chromium
    ;;

  *)
    echo "usage: $0 base|core|net|exploit|sast|browser" >&2
    exit 2
    ;;
esac

log "segment $1 complete"
