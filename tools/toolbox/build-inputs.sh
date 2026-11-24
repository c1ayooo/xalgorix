#!/usr/bin/env bash
# Content hash of everything that determines the toolbox image bytes.
#
# `make image-toolbox` stamps this hash into the image as a label and skips the
# build when the local image already carries the same value. The hash therefore
# has to cover exactly the inputs Docker would react to, and nothing else:
#
#   * the build recipe (Dockerfile, .dockerignore) and the files it COPYs into
#     the image (install.sh, versions.lock, toolpack.yaml);
#   * the Go sources compiled into the in-image helpers (the gobuild stage
#     copies only cmd/toolshim, cmd/httpworker, tools/toolbox/selftest plus
#     go.mod/go.sum — these binaries are stdlib-only by design);
#   * the profile and the apt mirror (they change which packages get installed);
#   * the digests of the base images, when they are already present locally
#     (a pulled base changes the produced image just like a source edit does);
#   * the builder flavour (BuildKit vs the deprecated legacy builder): their
#     caches are separate, so switching legitimately means one fresh build.
#
# GOPROXY is deliberately NOT an input: versions.lock pins every tool version,
# so the proxy only decides where identical sources are fetched from. Hashing it
# would make the image rebuild whenever a shell exported a different mirror.
#
# It deliberately does NOT hash the rest of the repository: platform code
# (internal/, cmd/xalgorix) never reaches the image, and touching it must not
# trigger a rebuild.
#
# Usage: build-inputs.sh <profile> [kali-mirror]
set -euo pipefail

profile=${1:?usage: build-inputs.sh <profile> [kali-mirror]}
kali_mirror=${2:-}

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

# Base image digests are part of the recipe. Absent locally means empty, which
# just makes the first build (that pulls them) restamp the hash.
base_digest() {
	if command -v docker >/dev/null 2>&1; then
		docker image inspect --format '{{.Id}}' "$1" 2>/dev/null || true
	fi
}

# Legacy and BuildKit caches cannot see each other. Hashing the builder makes a
# `docker build` that switched engines rebuild instead of reusing a label that
# describes a cache it will not hit.
builder_flavour() {
	if command -v docker >/dev/null 2>&1 && docker buildx version >/dev/null 2>&1; then
		echo buildkit
	else
		echo legacy
	fi
}

hash_files() {
	for f in "$@"; do
		[ -f "$f" ] || { printf 'missing\0%s\0' "$f"; continue; }
		printf 'file\0%s\0' "$f"
		cat "$f"
		printf '\0'
	done
}

hash_tree() {
	find "$1" -type f -print0 2>/dev/null | LC_ALL=C sort -z | {
		while IFS= read -r -d '' p; do
			printf 'file\0%s\0' "$p"
			cat "$p"
			printf '\0'
		done
	}
}

{
	printf 'profile\0%s\0' "$profile"
	printf 'kali_mirror\0%s\0' "$kali_mirror"
	printf 'builder\0%s\0' "$(builder_flavour)"
	printf 'base\0golang:1.26-bookworm\0%s\0' "$(base_digest golang:1.26-bookworm)"
	printf 'base\0kalilinux/kali-rolling\0%s\0' "$(base_digest kalilinux/kali-rolling)"
	hash_files tools/toolbox/Dockerfile .dockerignore \
		tools/toolbox/install.sh tools/toolbox/versions.lock tools/toolbox/toolpack.yaml \
		go.mod go.sum
	hash_tree cmd/toolshim
	hash_tree cmd/httpworker
	hash_tree tools/toolbox/selftest
} | sha256sum | cut -d' ' -f1
