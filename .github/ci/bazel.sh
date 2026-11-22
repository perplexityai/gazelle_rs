#!/usr/bin/env bash
set -euo pipefail

startup=()
while [[ $# -gt 0 && "$1" == -* ]]; do
  startup+=("$1")
  shift
done
[[ $# -gt 0 ]] || exec "$CI_CACHE_DIR/bin/bazelisk-real" ${startup[@]+"${startup[@]}"}
command=$1
shift

case "$command" in
  build|test|run|coverage|fetch)
    key=${BB_RO_KEY:-}
    upload=--noremote_upload_local_results
    # An overridden checkout in reusable CI must never use the writer key.
    if [[ "${GITHUB_EVENT_NAME:-}" == push && "${GITHUB_REF:-}" == refs/heads/main &&
          "${GITHUB_REPOSITORY:-}" == perplexityai/gazelle_rs &&
          "$(git -C "$GITHUB_WORKSPACE" rev-parse HEAD)" == "${GITHUB_SHA:-}" ]]; then
      : "${BB_RW_KEY:?Main CI requires the BuildBuddy writer key}"
      key=$BB_RW_KEY
      upload=--remote_upload_local_results
    fi
    remote=()
    if [[ -n "$key" ]]; then
      startup+=("--bazelrc=$GITHUB_WORKSPACE/.github/ci/buildbuddy.bazelrc")
      remote=(--config=ci "--remote_header=x-buildbuddy-api-key=$key"
        "--bes_header=x-buildbuddy-api-key=$key" "$upload"
        "--build_metadata=COMMIT_SHA=$GITHUB_SHA" "--build_metadata=CI_RUN_ID=$CI_RUN_ID")
    fi
    contents_cache=()
    # Bazel 9 repository contents can retain LLVM symlinks into old agents.
    if [[ "${USE_BAZEL_VERSION%%.*}" -ge 9 ]]; then
      contents_cache=(--repo_contents_cache=)
    fi
    exec "$CI_CACHE_DIR/bin/bazelisk-real" ${startup[@]+"${startup[@]}"} "$command" \
      --repository_cache="$CI_CACHE_DIR/repository" \
      ${remote[@]+"${remote[@]}"} ${contents_cache[@]+"${contents_cache[@]}"} "$@"
    ;;
  *) exec "$CI_CACHE_DIR/bin/bazelisk-real" ${startup[@]+"${startup[@]}"} "$command" "$@" ;;
esac
