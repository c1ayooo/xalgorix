#!/usr/bin/env bash
# Container entrypoint for the Xalgorix image.
#
# The image binds 0.0.0.0 (so the published port works), but the engine refuses
# to bind a non-loopback address without dashboard auth. Rather than fail a
# plain `docker run`, we generate a random admin password when none is supplied,
# print it once, and start. Operators can override by passing their own
# XALGORIX_USERNAME + XALGORIX_PASSWORD (or XALGORIX_PASSWORD_HASH).
set -euo pipefail

# Persist dashboard-configured settings on the data volume. The engine writes
# runtime settings (LLM model/key, integrations, etc.) to ~/.xalgorix.env
# (=/root/.xalgorix.env). Symlinking it onto the data directory means anything
# you set under Settings survives `docker run --rm` / container recreation, not
# just restarts. The directory follows XALGORIX_DATA_DIR: the split shape mounts
# a host directory at a different absolute path and would otherwise write this
# state into the image's anonymous /data volume.
DATA_DIR="${XALGORIX_DATA_DIR:-/data}"
mkdir -p "$DATA_DIR"
if [ ! -e "$DATA_DIR/.xalgorix.env" ]; then
  if [ -f /root/.xalgorix.env ] && [ ! -L /root/.xalgorix.env ]; then
    mv /root/.xalgorix.env "$DATA_DIR/.xalgorix.env"
  else
    : >"$DATA_DIR/.xalgorix.env"
  fi
fi
ln -sf "$DATA_DIR/.xalgorix.env" /root/.xalgorix.env

# Persist the traffic-proxy CA (R2b) on the data volume the same way: the
# interception CA (~/.xalgorix/ca, default XALGORIX_CA_DIR) is generated once
# and pinned by every consumer trust store (curl --cacert, python bundles,
# browser), so it must survive container recreation. -m 700 matters: the
# engine's EnsureSecureDir does NOT tighten the mode of an already-existing
# directory, so a loose data/ca would stay loose across restarts.
mkdir -m 700 -p "$DATA_DIR/ca" /root/.xalgorix
if [ ! -L /root/.xalgorix/ca ]; then
  if [ -d /root/.xalgorix/ca ] && [ -n "$(ls -A /root/.xalgorix/ca 2>/dev/null)" ] && [ ! -n "$(ls -A "$DATA_DIR/ca" 2>/dev/null)" ]; then
    mv /root/.xalgorix/ca/* "$DATA_DIR/ca/" 2>/dev/null || true
    rmdir /root/.xalgorix/ca
  fi
  ln -sf "$DATA_DIR/ca" /root/.xalgorix/ca
fi

bind="${XALGORIX_BIND:-0.0.0.0}"

# Is the bind address loopback (no auth required by the engine)?
is_loopback=false
case "$bind" in
  127.0.0.1 | localhost | ::1 | "") is_loopback=true ;;
esac

# Is dashboard auth already configured?
has_auth=false
if [ -n "${XALGORIX_USERNAME:-}" ] && { [ -n "${XALGORIX_PASSWORD:-}" ] || [ -n "${XALGORIX_PASSWORD_HASH:-}" ]; }; then
  has_auth=true
fi

if [ "$is_loopback" = false ] && [ "$has_auth" = false ]; then
  export XALGORIX_USERNAME="${XALGORIX_USERNAME:-admin}"
  export XALGORIX_PASSWORD="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
  echo "============================================================"
  echo "  Xalgorix — no dashboard auth was provided."
  echo "  Generated one-time credentials so the container can start:"
  echo ""
  echo "      username: ${XALGORIX_USERNAME}"
  echo "      password: ${XALGORIX_PASSWORD}"
  echo ""
  echo "  Override by setting XALGORIX_USERNAME + XALGORIX_PASSWORD"
  echo "  (or XALGORIX_PASSWORD_HASH). Rotate these before exposing"
  echo "  the dashboard on an untrusted network."
  echo "============================================================"
fi

exec xalgorix "$@"
