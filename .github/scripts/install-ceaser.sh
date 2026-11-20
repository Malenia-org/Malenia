#!/usr/bin/env bash
# Standalone installer used by the setup action. Never builds target source.
set -euo pipefail

version="${CEASER_VERSION:-v9.1}"
if [[ ! "$version" =~ ^(latest|v[0-9A-Za-z._-]+)$ ]]; then
  echo 'Ceaser version must be latest or a release tag beginning with v' >&2
  exit 2
fi
if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo 'The prebuilt Ceaser action currently requires Linux x86_64.' >&2
  exit 2
fi
: "${RUNNER_TEMP:?RUNNER_TEMP must be set}"
: "${GITHUB_PATH:?GITHUB_PATH must be set}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT must be set}"
install_dir="$(mktemp -d "$RUNNER_TEMP/ceaser-install.XXXXXX")"
if [[ "$version" == latest ]]; then
  base_url='https://github.com/caeser-org/ceaser/releases/latest/download'
else
  base_url="https://github.com/caeser-org/ceaser/releases/download/$version"
fi
download() {
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --retry 3 "$base_url/$1" -o "$install_dir/$1"
}
download ceaser_linux_amd64
download SHA256SUMS
# Only verify the requested asset; reject missing or duplicate entries.
awk '$2 == "ceaser_linux_amd64" {print; n++} END {if (n != 1) exit 1}' \
  "$install_dir/SHA256SUMS" > "$install_dir/selected.sha256"
(cd "$install_dir" && sha256sum --strict --check selected.sha256)
mv "$install_dir/ceaser_linux_amd64" "$install_dir/ceaser"
chmod 0755 "$install_dir/ceaser"
"$install_dir/ceaser" version
echo "$install_dir" >> "$GITHUB_PATH"
echo "binary-path=$install_dir/ceaser" >> "$GITHUB_OUTPUT"
