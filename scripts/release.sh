#!/usr/bin/env bash
# Builds the Linux release archives for one tag:
#   scripts/release.sh go-v1.2.3 dist/go-v1.2.3
# Produces codex-lb-<tag>-linux-{amd64,arm64}.tar.gz and SHA256SUMS. The
# archive layout is the one accepted by internal/adapters/releases (root
# codex-lb plus LICENSE, LICENSE.codex-relay and codex-lb.service). Archives
# are deterministic: two builds of the same commit produce identical checksums.
# Set SKIP_WEB_BUILD=1 when the dashboard was already built into
# internal/adapters/webui/dist. Nothing is published by this script.
set -euo pipefail

version="${1:?usage: scripts/release.sh <go-vX.Y.Z> <output-dir>}"
output="${2:?usage: scripts/release.sh <go-vX.Y.Z> <output-dir>}"
if [[ ! "$version" =~ ^go-v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
	echo "version must look like go-v1.2.3, got $version" >&2
	exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [[ "${SKIP_WEB_BUILD:-0}" != "1" ]]; then
	npm --prefix web run build
fi
if [[ ! -s internal/adapters/webui/dist/index.html ]]; then
	echo "dashboard assets are missing; run make web-deps web-build first" >&2
	exit 1
fi

epoch="$(git log -1 --format=%ct)"
mkdir -p "$output"
output="$(cd "$output" && pwd)"
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT

for arch in amd64 arm64; do
	dir="$stage/linux-$arch"
	mkdir -p "$dir"
	CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
		-ldflags "-s -w -X main.version=$version" -o "$dir/codex-lb" ./cmd/codex-lb
	install -m 0644 LICENSE "$dir/LICENSE"
	install -m 0644 internal/adapters/upstream/LICENSE.codex-relay "$dir/LICENSE.codex-relay"
	install -m 0644 deploy/codex-lb.service "$dir/codex-lb.service"
	chmod 0755 "$dir/codex-lb"
	archive="codex-lb-$version-linux-$arch.tar.gz"
	tar --sort=name --owner=0 --group=0 --numeric-owner --mtime="@$epoch" --format=gnu \
		-C "$dir" -cf - codex-lb LICENSE LICENSE.codex-relay codex-lb.service | gzip -n -9 >"$output/$archive"
done

(cd "$output" && sha256sum codex-lb-"$version"-linux-amd64.tar.gz codex-lb-"$version"-linux-arm64.tar.gz >SHA256SUMS)

if [[ "$(go env GOHOSTARCH)" == "amd64" ]]; then
	reported="$("$stage/linux-amd64/codex-lb" version)"
	if [[ "$reported" != "$version" ]]; then
		echo "built binary reports $reported, expected $version" >&2
		exit 1
	fi
fi
cat "$output/SHA256SUMS"
