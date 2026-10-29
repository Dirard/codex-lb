#!/usr/bin/env bash
# Runs one shard of the race-detector tests:
#   scripts/race-shard.sh <shard> <shards>
# Every top-level test, fuzz seed and example of the module belongs to exactly
# one shard: the sorted (package, name) pairs are dealt round-robin, so the
# slow packages (sqlite, httpapi, application) spread over all shards. A name
# shared by several packages runs in each of them in its shard, so nothing is
# skipped. Extra `go test` flags may follow the two numbers.
set -euo pipefail

shard="${1:?usage: scripts/race-shard.sh <shard> <shards> [go test flags]}"
shards="${2:?usage: scripts/race-shard.sh <shard> <shards> [go test flags]}"
shift 2
if [[ ! "$shard" =~ ^[1-9][0-9]*$ || ! "$shards" =~ ^[1-9][0-9]*$ ]] || ((shard > shards)); then
	echo "shard must be in 1..shards, got $shard of $shards" >&2
	exit 2
fi

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

names=()
index=0
while read -r _ files; do
	[[ -n "$files" ]] || continue
	# shellcheck disable=SC2086 # files is a list of paths without spaces
	while read -r name; do
		if ((index % shards == shard - 1)); then
			names+=("$name")
		fi
		index=$((index + 1))
	done < <(grep -hoE '^func (Test|Fuzz|Example)[A-Za-z0-9_]*\(' $files | sed -E 's/^func ([A-Za-z0-9_]+)\(/\1/' | grep -vx 'TestMain' | sort -u)
done < <(go list -race -f '{{.ImportPath}}{{range .TestGoFiles}} {{$.Dir}}/{{.}}{{end}}{{range .XTestGoFiles}} {{$.Dir}}/{{.}}{{end}}' ./... | sort)

if ((${#names[@]} == 0)); then
	echo "shard $shard of $shards has no tests" >&2
	exit 1
fi
pattern="^($(IFS='|'; echo "${names[*]}"))\$"
echo "shard $shard of $shards: ${#names[@]} of $index tests"
exec go test -race -timeout 30m -run "$pattern" "$@" ./...
