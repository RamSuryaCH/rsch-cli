#!/usr/bin/env bash
set -euo pipefail

tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/rsch-e2e.XXXXXX")
trap 'rm -rf "$tmp_dir"' EXIT

binary="$tmp_dir/rsch"
go build -trimpath -o "$binary" .

version="$($binary --version)"
if [[ -z "$version" || "$version" == "unknown" ]]; then
	printf 'invalid version output: %s\n' "$version" >&2
	exit 1
fi

help="$($binary --help)"
for flag in --auto-approve --yes --output-format --prompt; do
	if ! grep -Fq -- "$flag" <<<"$help"; then
		printf 'help is missing %s\n' "$flag" >&2
		exit 1
	fi
done

if "$binary" --definitely-unknown-flag >/dev/null 2>&1; then
	printf 'unknown flags unexpectedly succeeded\n' >&2
	exit 1
fi

if "$binary" --output-format definitely-invalid -p x >/dev/null 2>&1; then
	printf 'invalid output format unexpectedly succeeded\n' >&2
	exit 1
fi

printf 'CLI E2E smoke passed (%s)\n' "$version"
