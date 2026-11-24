#!/usr/bin/env bash
set -euo pipefail

dist_dir=${1:-dist}
if [[ ! -d "${dist_dir}" ]]; then
    printf 'release directory does not exist: %s\n' "${dist_dir}" >&2
    exit 1
fi

for dependency in awk grep tar unzip; do
    command -v "${dependency}" >/dev/null 2>&1 || {
        printf '%s is required to validate release archives\n' "${dependency}" >&2
        exit 1
    }
done

expected_archives=(
    rsch-linux-x86_64.tar.gz
    rsch-linux-arm64.tar.gz
    rsch-mac-x86_64.tar.gz
    rsch-mac-arm64.tar.gz
    rsch-windows-x86_64.zip
    rsch-windows-arm64.zip
)

for archive in "${expected_archives[@]}"; do
    archive_path="${dist_dir}/${archive}"
    if [[ ! -f "${archive_path}" ]]; then
        printf 'missing release archive: %s\n' "${archive}" >&2
        exit 1
    fi
    case "${archive}" in
        *.tar.gz)
            tar -tzf "${archive_path}" | grep -Fqx 'rsch' || {
                printf '%s does not contain the rsch executable at archive root\n' "${archive}" >&2
                exit 1
            }
            ;;
        *.zip)
            unzip -Z1 "${archive_path}" | grep -Fqx 'rsch.exe' || {
                printf '%s does not contain rsch.exe at archive root\n' "${archive}" >&2
                exit 1
            }
            ;;
    esac
done

if [[ ! -f "${dist_dir}/checksums.txt" ]]; then
    printf 'missing checksums.txt\n' >&2
    exit 1
fi

# A valid subset of checksums must not leave shipped archives unverified.
for archive in "${expected_archives[@]}"; do
    matching_checksums=$(awk -v file="${archive}" '$2 == file { print $1 }' "${dist_dir}/checksums.txt")
    if [[ ! "${matching_checksums}" =~ ^[[:xdigit:]]{64}$ ]]; then
        printf 'expected exactly one SHA-256 checksum for %s\n' "${archive}" >&2
        exit 1
    fi
done

(
    cd "${dist_dir}"
    if command -v sha256sum >/dev/null 2>&1; then
        sha256sum -c checksums.txt
    elif command -v shasum >/dev/null 2>&1; then
        shasum -a 256 -c checksums.txt
    else
        printf 'sha256sum or shasum is required to validate checksums\n' >&2
        exit 1
    fi
)

printf 'Release archive contract test passed\n'
