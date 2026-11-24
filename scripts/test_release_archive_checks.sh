#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/rsch-archive-test.XXXXXX")
trap 'rm -rf -- "${test_root}"' EXIT
dist_dir="${test_root}/release files"
payload_dir="${test_root}/payload"
mkdir -p "${dist_dir}" "${payload_dir}"
printf '#!/bin/sh\nexit 0\n' >"${payload_dir}/rsch"
chmod 0755 "${payload_dir}/rsch"
cp "${payload_dir}/rsch" "${payload_dir}/rsch.exe"

for target in linux-x86_64 linux-arm64 mac-x86_64 mac-arm64; do
    tar -czf "${dist_dir}/rsch-${target}.tar.gz" -C "${payload_dir}" rsch
done
for target in windows-x86_64 windows-arm64; do
    (cd "${payload_dir}" && zip -q "${dist_dir}/rsch-${target}.zip" rsch.exe)
done

(
    cd "${dist_dir}"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum ./*.tar.gz ./*.zip
    else
        shasum -a 256 ./*.tar.gz ./*.zip
    fi
) | sed 's|  \./|  |' >"${dist_dir}/checksums.txt"
cp "${dist_dir}/checksums.txt" "${test_root}/valid-checksums.txt"
bash "${repo_root}/scripts/test_release_archives.sh" "${dist_dir}" >/dev/null

assert_rejected() {
    local message=$1
    if bash "${repo_root}/scripts/test_release_archives.sh" "${dist_dir}" >"${test_root}/validation.log" 2>&1; then
        printf 'archive validation accepted %s\n' "${message}" >&2
        exit 1
    fi
}

# sha256sum -c succeeds on a valid subset; every expected archive needs coverage.
sed '/rsch-windows-arm64.zip$/d' "${test_root}/valid-checksums.txt" >"${dist_dir}/checksums.txt"
assert_rejected 'an archive missing from the checksum manifest'
grep -Fq 'expected exactly one SHA-256 checksum for rsch-windows-arm64.zip' "${test_root}/validation.log"

cp "${test_root}/valid-checksums.txt" "${dist_dir}/checksums.txt"
head -n 1 "${test_root}/valid-checksums.txt" >>"${dist_dir}/checksums.txt"
assert_rejected 'duplicate checksum entries'
grep -Fq 'expected exactly one SHA-256 checksum' "${test_root}/validation.log"

awk 'NR == 1 { $1 = "0000000000000000000000000000000000000000000000000000000000000000" } { printf "%s  %s\n", $1, $2 }' \
    "${test_root}/valid-checksums.txt" >"${dist_dir}/checksums.txt"
assert_rejected 'an incorrect checksum'
grep -Fq 'FAILED' "${test_root}/validation.log"

cp "${test_root}/valid-checksums.txt" "${dist_dir}/checksums.txt"
rm "${dist_dir}/rsch-mac-arm64.tar.gz"
assert_rejected 'a missing platform archive'
grep -Fq 'missing release archive: rsch-mac-arm64.tar.gz' "${test_root}/validation.log"

printf 'Release archive validator tests passed\n'
