#!/usr/bin/env bash
#
# Run the PostgreSQL integration suites against an isolated, temporary
# PostgreSQL container. The container, database and password are created for
# this invocation only; the trap removes everything on success, failure or
# interruption. No credential is written to the repository or printed.
#
# Usage:
#   scripts/test-postgres.sh
#   scripts/test-postgres.sh ./internal/platform ./internal/infoitem/postgres
#
# Override the image with XALGORIX_TEST_POSTGRES_IMAGE if the default cannot be
# pulled (for example in an offline environment with a pinned local image).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

if ! command -v docker >/dev/null 2>&1; then
  echo "test-postgres: docker is required" >&2
  exit 1
fi
if ! docker info >/dev/null 2>&1; then
  echo "test-postgres: cannot talk to the Docker daemon" >&2
  exit 1
fi

image="${XALGORIX_TEST_POSTGRES_IMAGE:-postgres:16-alpine}"
container="xalgorix-pgtest-$$-$(date +%s)"
db_user="xalgorix"
db_name="xalgorix_test"
# 192 random bits; never echoed and only exported in the child DSN.
db_password="$(openssl rand -hex 24 2>/dev/null || od -An -N24 -tx1 /dev/urandom | tr -d ' \n')"
if [ -z "$db_password" ]; then
  echo "test-postgres: could not generate a temporary database password" >&2
  exit 1
fi

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

echo "test-postgres: starting temporary ${image} container"
if ! docker run --rm -d \
  --name "$container" \
  -e POSTGRES_USER="$db_user" \
  -e POSTGRES_PASSWORD="$db_password" \
  -e POSTGRES_DB="$db_name" \
  -p 127.0.0.1::5432 \
  "$image" >/dev/null; then
  echo "test-postgres: failed to start PostgreSQL container" >&2
  exit 1
fi

ready=0
for _ in $(seq 1 60); do
  if docker exec "$container" pg_isready -U "$db_user" -d "$db_name" >/dev/null 2>&1; then
    ready=1
    break
  fi
  # A crashed/exited container will never become ready; fail fast.
  if [ "$(docker inspect -f '{{.State.Running}}' "$container" 2>/dev/null || echo false)" != "true" ]; then
    break
  fi
  sleep 1
done
if [ "$ready" -ne 1 ]; then
  echo "test-postgres: PostgreSQL did not become ready" >&2
  docker logs "$container" 2>&1 | tail -40 >&2 || true
  exit 1
fi

mapping="$(docker port "$container" 5432/tcp | head -n1)"
host_port="${mapping##*:}"
if ! [[ "$host_port" =~ ^[0-9]+$ ]]; then
  echo "test-postgres: could not resolve the published PostgreSQL port" >&2
  exit 1
fi

export XALGORIX_TEST_DATABASE_URL="postgres://${db_user}:${db_password}@127.0.0.1:${host_port}/${db_name}?sslmode=disable"

packages=("$@")
if [ "${#packages[@]}" -eq 0 ]; then
  packages=(
    ./internal/platform
    ./internal/infoitem/postgres
    ./internal/agent
  )
fi

echo "test-postgres: running integration suites against 127.0.0.1:${host_port}"
# Serial execution avoids competing migrations/DDL across packages sharing one
# database. Each test suite still creates unique project/scan IDs.
go test -count=1 -p 1 "${packages[@]}"
