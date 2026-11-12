#!/usr/bin/env bash
set -euo pipefail

: "${TARGET_REPOSITORY:?TARGET_REPOSITORY is required}"
: "${HEAD_SHA:?HEAD_SHA is required}"
: "${RUNNER_TEMP:?RUNNER_TEMP is required}"
: "${GITHUB_OUTPUT:?GITHUB_OUTPUT is required}"

run_id="${SOURCE_RUN_ID:-}"
source_url="${SOURCE_CI_URL:-}"
status_file="$RUNNER_TEMP/source-evidence-status.txt"
mkdir -p "$RUNNER_TEMP/source-artifacts"
prow_max_artifact_bytes="${PROW_MAX_ARTIFACT_BYTES:-268435456}"

write_github_artifact_manifest() {
  local metadata="$RUNNER_TEMP/source-artifacts/.github-artifacts.json"
  local entries="$RUNNER_TEMP/source-artifacts/.malenia-artifact-entries.ndjson"
  if ! gh api "repos/$TARGET_REPOSITORY/actions/runs/$run_id/artifacts?per_page=100" > "$metadata"; then
    echo "artifact_manifest=unavailable" >> "$status_file"
    return 0
  fi
  : > "$entries"
  while IFS=$'\t' read -r artifact_id artifact_name artifact_digest artifact_ref; do
    [ -n "$artifact_name" ] || continue
    artifact_dir="$RUNNER_TEMP/source-artifacts/$artifact_name"
    [ -d "$artifact_dir" ] || continue
    while IFS= read -r -d '' file; do
      relative="${file#"$RUNNER_TEMP/source-artifacts/"}"
      jq -cn \
        --arg path "$relative" \
        --arg artifact_id "github-actions:$artifact_id" \
        --arg digest "$artifact_digest" \
        --arg storage_ref "$artifact_ref" \
        '{path:$path, artifact_id:$artifact_id, digest:$digest, storage_ref:$storage_ref}' >> "$entries"
    done < <(find "$artifact_dir" -type f -print0)
  done < <(jq -r '.artifacts[]? | [(.id|tostring), .name, (.digest // ""), (.archive_download_url // "")] | @tsv' "$metadata")
  jq -s '{artifacts: .}' "$entries" > "$RUNNER_TEMP/source-artifacts/.malenia-artifacts.json"
  rm -f "$metadata" "$entries"
  echo "artifact_manifest=created" >> "$status_file"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

urlencode() {
  jq -rn --arg value "$1" '$value | @uri'
}

write_prow_artifact_manifest() {
  local source_prefix="${source_url#https://prow.k8s.io/view/gs/}"
  local bucket="${source_prefix%%/*}"
  local object_prefix="${source_prefix#*/}"
  local listing="$RUNNER_TEMP/source-artifacts/.prow-gcs-objects.jsonl"
  local entries="$RUNNER_TEMP/source-artifacts/.prow-artifact-entries.ndjson"
  local page_token=""
  local response=""
  local list_url=""
  local next_token=""

  : > "$listing"
  while :; do
    list_url="https://storage.googleapis.com/storage/v1/b/$bucket/o?prefix=$(urlencode "$object_prefix/")&fields=items(name,size,contentType,md5Hash,crc32c,generation),nextPageToken"
    if [ -n "$page_token" ]; then
      list_url="$list_url&pageToken=$(urlencode "$page_token")"
    fi
    if ! response="$(curl --fail --silent --show-error --location "$list_url")"; then
      echo "artifact_manifest=unavailable" >> "$status_file"
      echo "artifact_manifest_reason=public GCS object listing failed" >> "$status_file"
      return 0
    fi
    jq -c '.items[]?' <<<"$response" >> "$listing"
    next_token="$(jq -r '.nextPageToken // empty' <<<"$response")"
    [ -n "$next_token" ] || break
    page_token="$next_token"
  done

  local prow_head_revision=""
  local prow_checkout_revision=""
  local go_version=""
  local finished_json="$RUNNER_TEMP/source-artifacts/prow-metadata/finished.json"
  local clone_records="$RUNNER_TEMP/source-artifacts/prow-metadata/clone-records.json"
  local downloaded=0
  local skipped=0
  local binary_available=false
  mkdir -p "$RUNNER_TEMP/source-artifacts/prow-metadata"

  while IFS=$'\t' read -r object_name object_size content_type; do
    [ -n "$object_name" ] || continue
    local relative="${object_name#"$object_prefix/"}"
    local destination=""
    if [[ "$relative" == artifacts/* ]]; then
      destination="$RUNNER_TEMP/source-artifacts/$relative"
    else
      destination="$RUNNER_TEMP/source-artifacts/prow-metadata/${relative##*/}"
    fi
    if [ "${object_size:-0}" -gt "$prow_max_artifact_bytes" ]; then
      skipped=$((skipped + 1))
      echo "artifact_skipped=$relative:size_limit" >> "$status_file"
      continue
    fi
    mkdir -p "$(dirname "$destination")"
    if ! curl --fail --silent --show-error --location \
      "https://storage.googleapis.com/$bucket/$object_name" > "$destination"; then
      echo "artifact_skipped=$relative:download_failed" >> "$status_file"
      continue
    fi
    downloaded=$((downloaded + 1))
    if [[ "$relative" == artifacts/race-binaries/* ]]; then
      binary_available=true
    fi
  done < <(jq -r '[.name, (.size // "0"), (.contentType // "application/octet-stream")] | @tsv' "$listing")

  if [ -s "$finished_json" ]; then
    prow_head_revision="$(jq -r '.revision // empty' "$finished_json")"
  fi
  if [ -s "$clone_records" ]; then
    prow_checkout_revision="$(jq -r '[.[]?.final_sha // empty] | map(select(length > 0)) | last // empty' "$clone_records")"
  fi
  go_version="$(grep -Eo 'GOTOOLCHAIN=go[0-9.]+ ' "$RUNNER_TEMP/source-ci.log" | head -n 1 | tr -d ' ' | sed 's/^GOTOOLCHAIN=//' || true)"

  : > "$entries"
  while IFS=$'\t' read -r object_name object_size content_type; do
    [ -n "$object_name" ] || continue
    local relative="${object_name#"$object_prefix/"}"
    local relative_path=""
    if [[ "$relative" == artifacts/* ]]; then
      relative_path="$relative"
    else
      relative_path="prow-metadata/${relative##*/}"
    fi
    local path="$RUNNER_TEMP/source-artifacts/$relative_path"
    [ -f "$path" ] || continue
    local digest
    digest="$(sha256_file "$path")"
    local kind=""
    local race_enabled=false
    local binary_build_id=""
    local binary_symbols=""
    local binary_dwarf=""
    local binary_goos=""
    local binary_goarch=""
    local binary_go_version="$go_version"
    if [[ "$relative_path" == artifacts/race-binaries/* ]]; then
      kind="go_race_test_binary"
      race_enabled=true
      if command -v go >/dev/null 2>&1; then
        binary_build_id="$(go tool buildid "$path" 2>/dev/null || true)"
        build_metadata="$(go version -m "$path" 2>/dev/null || true)"
        binary_go_version="$(sed -n '1s/.*: //p' <<<"$build_metadata")"
        binary_goos="$(awk '$1 == "build" && $2 == "GOOS" {print $3; exit}' <<<"$build_metadata")"
        binary_goarch="$(awk '$1 == "build" && $2 == "GOARCH" {print $3; exit}' <<<"$build_metadata")"
        if grep -qE '^build[[:space:]]+-race[[:space:]]+true$' <<<"$build_metadata"; then
          race_enabled=true
        fi
      fi
      if command -v go >/dev/null 2>&1 && go tool nm "$path" >/dev/null 2>&1; then
        binary_symbols="embedded"
      fi
      if command -v readelf >/dev/null 2>&1 && readelf -S "$path" 2>/dev/null | grep -qE '\.debug_(info|line)'; then
        binary_dwarf="embedded"
      fi
    elif [[ "$relative_path" == prow-metadata/* ]]; then
      kind="prow_metadata"
    fi
    jq -cn \
      --arg path "$relative_path" \
      --arg artifact_id "prow:$build_id:${relative_path//\//:}" \
      --arg provider "prow" \
      --arg build_id "$build_id" \
      --arg source_revision "$prow_checkout_revision" \
      --arg requested_revision "$HEAD_SHA" \
      --arg prow_head_revision "$prow_head_revision" \
      --arg prow_checkout_revision "$prow_checkout_revision" \
      --arg storage_ref "https://storage.googleapis.com/$bucket/$object_name" \
      --arg media_type "$content_type" \
      --arg digest "$digest" \
      --arg kind "$kind" \
      --arg go_version "$binary_go_version" \
      --arg binary_build_id "$binary_build_id" \
      --arg symbols "$binary_symbols" \
      --arg dwarf "$binary_dwarf" \
      --arg goos "$binary_goos" \
      --arg goarch "$binary_goarch" \
      --argjson size "${object_size:-0}" \
      --argjson race_enabled "$race_enabled" \
      '{schema_version:2,path:$path,artifact_id:$artifact_id,provider:$provider,build_id:$build_id,source_revision:$source_revision,requested_revision:$requested_revision,prow_head_revision:$prow_head_revision,prow_checkout_revision:$prow_checkout_revision,storage_ref:$storage_ref,media_type:$media_type,size:$size,digest:$digest,kind:$kind,go_version:$go_version,binary_build_id:$binary_build_id,symbols:$symbols,dwarf:$dwarf,goos:$goos,goarch:$goarch,race_enabled:$race_enabled}' \
      >> "$entries"
  done < <(jq -r '[.name, (.size // "0"), (.contentType // "application/octet-stream")] | @tsv' "$listing")
  jq -s '{schema_version:2,provider:"prow",build_id:$build_id,artifacts:.}' --arg build_id "$build_id" "$entries" > "$RUNNER_TEMP/source-artifacts/.malenia-artifacts.json"
  rm -f "$listing" "$entries"
  echo "artifact_manifest=created" >> "$status_file"
  echo "artifact_downloaded=$downloaded" >> "$status_file"
  echo "artifact_skipped_count=$skipped" >> "$status_file"
  if [ "$binary_available" = true ]; then
    echo "race_binary=available" >> "$status_file"
  else
    echo "race_binary=unavailable" >> "$status_file"
  fi
}
{
  echo "target_repository=$TARGET_REPOSITORY"
  echo "head_sha=$HEAD_SHA"
  echo "requested_source_run_id=${run_id:-none}"
  echo "requested_source_ci_url=${source_url:-none}"
} > "$status_file"

evidence_failure() {
  local reason="$1"
  echo "status=failure" >> "$status_file"
  echo "reason=$reason" >> "$status_file"
  echo "available=false" >> "$GITHUB_OUTPUT"
}

if [ -n "$source_url" ] && [[ "$source_url" != https://prow.k8s.io/view/gs/* ]]; then
  evidence_failure "source_ci_url is not a supported Prow GS URL"
  exit 0
fi

if [ -z "$run_id" ] && [ -z "$source_url" ]; then
  run_id="$(gh run list --repo "$TARGET_REPOSITORY" --commit "$HEAD_SHA" \
    --status failure --limit 1 --json databaseId --jq '.[0].databaseId // empty')"
fi
if [ -z "$run_id" ] && [ -z "$source_url" ]; then
  source_url="$(gh api --paginate "repos/$TARGET_REPOSITORY/commits/$HEAD_SHA/status" \
    --jq '.statuses[] | select(.state == "failure") | .target_url' \
    | grep '^https://prow.k8s.io/view/gs/' | sed -n '1p' || true)"
fi

if [ -z "$run_id" ]; then
  if [[ "$source_url" != https://prow.k8s.io/view/gs/* ]]; then
    evidence_failure "No failed source CI run or supported Prow status context found for $TARGET_REPOSITORY@$HEAD_SHA"
    exit 0
  fi
  source_url="${source_url%/}"
  build_id="${source_url##*/}"
  log_url="https://storage.googleapis.com/${source_url#https://prow.k8s.io/view/gs/}/build-log.txt"
  {
    echo "provider=prow"
    echo "source_url=$source_url"
    echo "build_id=$build_id"
    echo "log_url=$log_url"
  } >> "$status_file"
  if ! curl --fail --silent --show-error --location "$log_url" > "$RUNNER_TEMP/source-ci.log"; then
    evidence_failure "Could not read Prow log: $log_url"
    exit 0
  fi
  write_prow_artifact_manifest
  source_provider="prow"
else
  if ! gh run view "$run_id" --repo "$TARGET_REPOSITORY" --log-failed \
    > "$RUNNER_TEMP/source-ci.log"; then
    evidence_failure "Could not read failed-job log for source CI run $run_id"
    exit 0
  fi
  gh run download "$run_id" --repo "$TARGET_REPOSITORY" \
    --dir "$RUNNER_TEMP/source-artifacts" || echo "artifact_download=failed" >> "$status_file"
  write_github_artifact_manifest
  {
    echo "provider=github-actions"
    echo "run_id=$run_id"
    echo "log_path=$RUNNER_TEMP/source-ci.log"
    echo "artifact_dir=$RUNNER_TEMP/source-artifacts"
  } >> "$status_file"
  source_provider="github-actions"
fi

if [ -z "$run_id" ] && [ -z "$source_url" ]; then
  evidence_failure "No failed source CI run found for $TARGET_REPOSITORY@$HEAD_SHA"
  exit 0
fi
if [ ! -s "$RUNNER_TEMP/source-ci.log" ]; then
  evidence_failure "Source CI evidence was empty"
  exit 0
fi

sed -E \
  -e 's/(authorization:[[:space:]]*bearer[[:space:]]+)[^[:space:]]+/\1[REDACTED]/Ig' \
  -e 's/((token|password|secret|api[_-]?key)[=:][[:space:]]*)[^[:space:]]+/\1[REDACTED]/Ig' \
  -e 's/(gh[pousr]_[A-Za-z0-9_]+|github_pat_[A-Za-z0-9_]+|AKIA[0-9A-Z]{16})/[REDACTED]/g' \
  "$RUNNER_TEMP/source-ci.log" > "$RUNNER_TEMP/source-ci-sanitized.log"
{
  head -c 131072 "$RUNNER_TEMP/source-ci-sanitized.log"
  printf '\n[... CI evidence middle omitted ...]\n'
  tail -c 131072 "$RUNNER_TEMP/source-ci-sanitized.log"
} > "$RUNNER_TEMP/source-ci-bounded.log"

echo "source_url=$source_url" >> "$GITHUB_OUTPUT"
echo "provider=$source_provider" >> "$GITHUB_OUTPUT"
echo "run_id=$run_id" >> "$GITHUB_OUTPUT"
echo "build_id=${build_id:-}" >> "$GITHUB_OUTPUT"
echo "log_path=$RUNNER_TEMP/source-ci.log" >> "$GITHUB_OUTPUT"
echo "bounded_log_path=$RUNNER_TEMP/source-ci-bounded.log" >> "$GITHUB_OUTPUT"
echo "artifact_dir=$RUNNER_TEMP/source-artifacts" >> "$GITHUB_OUTPUT"
echo "race_binary_status=$(grep -E '^race_binary=' "$status_file" | tail -n 1 | cut -d= -f2- || echo unavailable)" >> "$GITHUB_OUTPUT"
echo "status=success" >> "$status_file"
echo "available=true" >> "$GITHUB_OUTPUT"
