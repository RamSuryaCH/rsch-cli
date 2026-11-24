#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/rsch-install-test.XXXXXX")
trap 'rm -rf -- "${test_root}"' EXIT

release_dir="${test_root}/release"
payload_dir="${test_root}/payload"
mock_bin="${test_root}/mock-bin"
test_home="${test_root}/home"
mkdir -p "${release_dir}" "${payload_dir}" "${mock_bin}" "${test_home}"

printf '%s\n' '#!/bin/sh' 'if [ "${1:-}" = "--version" ]; then' '  printf "%s\\n" "1.2.3"' 'fi' >"${payload_dir}/rsch"
chmod 0755 "${payload_dir}/rsch"
tar -czf "${release_dir}/rsch-linux-x86_64.tar.gz" -C "${payload_dir}" rsch

if command -v sha256sum >/dev/null 2>&1; then
    archive_checksum=$(sha256sum "${release_dir}/rsch-linux-x86_64.tar.gz" | awk '{print $1}')
else
    archive_checksum=$(shasum -a 256 "${release_dir}/rsch-linux-x86_64.tar.gz" | awk '{print $1}')
fi
printf '%s  %s\n' "${archive_checksum}" 'rsch-linux-x86_64.tar.gz' >"${release_dir}/checksums.txt"

printf '%s\n' '#!/usr/bin/env bash' 'case "${1:-}" in' '  -s) printf "%s\\n" Linux ;;' '  -m) printf "%s\\n" "${MOCK_ARCH:-x86_64}" ;;' '  *) exit 2 ;;' 'esac' >"${mock_bin}/uname"
chmod 0755 "${mock_bin}/uname"

common_env=(
    "HOME=${test_home}"
    "XDG_CONFIG_HOME=${test_home}/.config"
    "SHELL=/bin/bash"
    "VERSION=1.2.3"
    "RSCH_RELEASE_BASE_URL=file://${release_dir}"
    "RSCH_INSTALL_DIR=${test_home}/bin"
    "RSCH_NO_MODIFY_PATH=1"
    "GITHUB_ACTIONS=false"
)

env "${common_env[@]}" PATH="${mock_bin}:${PATH}" bash "${repo_root}/install" >/dev/null
installed_version=$("${test_home}/bin/rsch" --version)
if [[ "${installed_version}" != '1.2.3' ]]; then
    printf 'installed version mismatch: %s\n' "${installed_version}" >&2
    exit 1
fi

second_output=$(env "${common_env[@]}" PATH="${test_home}/bin:${mock_bin}:${PATH}" bash "${repo_root}/install")
if [[ "${second_output}" != *'already installed'* ]]; then
    printf 'idempotent install did not detect the installed version\n' >&2
    exit 1
fi

# An existing PATH installation must not suppress an explicit new destination.
env "${common_env[@]}" \
    RSCH_INSTALL_DIR="${test_home}/second-bin" \
    PATH="${test_home}/bin:${mock_bin}:${PATH}" \
    bash "${repo_root}/install" >/dev/null
[[ -x "${test_home}/second-bin/rsch" ]]

# The selected destination is idempotent even before it is added to PATH.
destination_output=$(env "${common_env[@]}" PATH="${mock_bin}:${PATH}" bash "${repo_root}/install")
[[ "${destination_output}" == *'already installed'* ]]

# A cached installation still needs PATH setup in a fresh shell/CI environment.
touch "${test_home}/.bashrc"
cached_output=$(env "${common_env[@]}" \
    RSCH_NO_MODIFY_PATH=0 \
    GITHUB_ACTIONS=true \
    GITHUB_PATH="${test_root}/github-path" \
    PATH="${mock_bin}:${PATH}" \
    bash "${repo_root}/install")
[[ "${cached_output}" == *'already installed'* && "${cached_output}" != *'Downloading'* ]]
grep -Fqx "${test_home}/bin" "${test_root}/github-path"
configured_binary=$(env "${common_env[@]}" PATH="${mock_bin}:${PATH}" \
    bash -c '. "$HOME/.bashrc"; command -v rsch')
[[ "${configured_binary}" == "${test_home}/bin/rsch" ]]

bad_release="${test_root}/bad-release"
mkdir -p "${bad_release}"
cp "${release_dir}/checksums.txt" "${bad_release}/checksums.txt"
cp "${release_dir}/rsch-linux-x86_64.tar.gz" "${bad_release}/rsch-linux-x86_64.tar.gz"
printf 'corrupt' >>"${bad_release}/rsch-linux-x86_64.tar.gz"
if env "${common_env[@]}" \
    RSCH_RELEASE_BASE_URL="file://${bad_release}" \
    RSCH_INSTALL_DIR="${test_home}/bad-bin" \
    PATH="${mock_bin}:${PATH}" \
    bash "${repo_root}/install" >"${test_root}/bad-checksum.log" 2>&1; then
    printf 'installer accepted an archive with a bad checksum\n' >&2
    exit 1
fi
grep -Fq 'Checksum verification failed' "${test_root}/bad-checksum.log"

if env "${common_env[@]}" \
    MOCK_ARCH=i386 \
    PATH="${mock_bin}:${PATH}" \
    bash "${repo_root}/install" >"${test_root}/unsupported.log" 2>&1; then
    printf 'installer accepted an unsupported i386 target\n' >&2
    exit 1
fi
grep -Fq 'Unsupported OS/Arch: linux/i386' "${test_root}/unsupported.log"

# Valid checksums alone do not make archive links or duplicate executables safe.
archive_fixture="${test_root}/archive-fixture"
mkdir -p "${archive_fixture}/payload" "${archive_fixture}/release"
write_fixture_checksum() {
    local archive="${archive_fixture}/release/rsch-linux-x86_64.tar.gz"
    local checksum
    if command -v sha256sum >/dev/null 2>&1; then
        checksum=$(sha256sum "${archive}" | awk '{print $1}')
    else
        checksum=$(shasum -a 256 "${archive}" | awk '{print $1}')
    fi
    printf '%s  %s\n' "${checksum}" 'rsch-linux-x86_64.tar.gz' >"${archive_fixture}/release/checksums.txt"
}
assert_fixture_rejected() {
    local expected_message=$1
    write_fixture_checksum
    if env "${common_env[@]}" \
        RSCH_RELEASE_BASE_URL="file://${archive_fixture}/release" \
        RSCH_INSTALL_DIR="${test_home}/archive-bin" \
        PATH="${mock_bin}:${PATH}" \
        bash "${repo_root}/install" >"${test_root}/archive.log" 2>&1; then
        printf 'installer accepted an unsafe executable archive\n' >&2
        exit 1
    fi
    grep -Fq "${expected_message}" "${test_root}/archive.log"
    [[ ! -e "${test_home}/archive-bin/rsch" ]]
}

ln -s "${payload_dir}/rsch" "${archive_fixture}/payload/rsch"
tar -czf "${archive_fixture}/release/rsch-linux-x86_64.tar.gz" -C "${archive_fixture}/payload" rsch
assert_fixture_rejected 'executable must be a regular file'

rm "${archive_fixture}/payload/rsch"
cp "${payload_dir}/rsch" "${archive_fixture}/payload/rsch"
tar -czf "${archive_fixture}/release/rsch-linux-x86_64.tar.gz" -C "${archive_fixture}/payload" rsch rsch
assert_fixture_rejected 'must contain exactly one rsch'

# Additional archive members do not interfere with selecting the executable.
ln -s "${test_root}" "${archive_fixture}/payload/extra-link"
tar -czf "${archive_fixture}/release/rsch-linux-x86_64.tar.gz" -C "${archive_fixture}/payload" rsch extra-link
write_fixture_checksum
env "${common_env[@]}" \
    RSCH_RELEASE_BASE_URL="file://${archive_fixture}/release" \
    RSCH_INSTALL_DIR="${test_home}/archive-bin" \
    PATH="${mock_bin}:${PATH}" \
    bash "${repo_root}/install" >/dev/null
[[ "$("${test_home}/archive-bin/rsch" --version)" == '1.2.3' ]]

printf 'Installer contract test passed\n'
