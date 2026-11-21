#!/usr/bin/env bash
# Standalone installer used by the setup action. Never builds target source.
set -euo pipefail

version="${CAESER_VERSION:-v9.1}"
if [[ ! "$version" =~ ^(latest|v[0-9A-Za-z._-]+)$ ]]; then
  echo 'Caeser version must be latest or a release tag beginning with v' >&2
  exit 2
fi
if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
  echo 'The prebuilt Caeser action currently requires Linux x86_64.' >&2
  exit 2
fi
: "${RUNNER_TEMP:?RUNNER_TEMP must be set}"
: "${GITHUB_PATH:?GITHUB_PATH must be set}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT must be set}"
install_dir="$(mktemp -d "$RUNNER_TEMP/caeser-install.XXXXXX")"
if [[ "$version" == latest ]]; then
  base_url='https://github.com/caeser-org/ceaser/releases/latest/download'
else
  base_url="https://github.com/caeser-org/ceaser/releases/download/$version"
fi
download() {
  curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
    --retry 3 "$base_url/$1" -o "$install_dir/$1"
}
download SHA256SUMS
# Prefer the new asset name; fall back to the name published by the v9.x
# releases so pinned older versions still install.
asset=""
for candidate in caeser_linux_amd64 ceaser_linux_amd64; do
  if curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location \
      --retry 3 "$base_url/$candidate" -o "$install_dir/$candidate"; then
    asset="$candidate"
    break
  fi
done
if [[ -z "$asset" ]]; then
  echo 'no prebuilt Caeser asset found for this version' >&2
  exit 2
fi
# Only verify the requested asset; reject missing or duplicate entries.
awk -v a="$asset" '$2 == a {print; n++} END {if (n != 1) exit 1}' \
  "$install_dir/SHA256SUMS" > "$install_dir/selected.sha256"
(cd "$install_dir" && sha256sum --strict --check selected.sha256)
mv "$install_dir/$asset" "$install_dir/caeser"
chmod 0755 "$install_dir/caeser"
"$install_dir/caeser" version
echo "$install_dir" >> "$GITHUB_PATH"
echo "binary-path=$install_dir/caeser" >> "$GITHUB_OUTPUT"
