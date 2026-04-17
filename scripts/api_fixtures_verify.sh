#!/usr/bin/env bash
set -euo pipefail

refresh=0
check_captures=0
case_id=""
target_id=""
provider_id=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --refresh)
      refresh=1
      shift
      ;;
    --check-captures)
      check_captures=1
      shift
      ;;
    --case|-case)
      case_id="${2:-}"
      shift 2
      ;;
    --target|-target)
      target_id="${2:-}"
      shift 2
      ;;
    --provider|-provider)
      provider_id="${2:-}"
      shift 2
      ;;
    *)
      echo "unknown argument: $1" >&2
      echo "usage: $0 [--refresh] [--check-captures] [--case <id>] [--provider <provider>] [--target <target>]" >&2
      exit 2
      ;;
  esac
done

capture_args=()
verify_args=()

if [[ -n "$case_id" ]]; then
  capture_args+=(-case "$case_id")
  verify_args+=(-case "$case_id")
fi

if [[ -n "$target_id" ]]; then
  capture_args+=(-target "$target_id")
  verify_args+=(-target "$target_id")
fi

if [[ -n "$provider_id" ]]; then
  capture_args+=(-provider "$provider_id")
  verify_args+=(-provider "$provider_id")
fi

if (( check_captures || refresh )); then
  verify_args+=(-check-captures)
fi

if (( refresh )); then
  go run ./cmd/apifixtures -- capture-all "${capture_args[@]}"
fi

go run ./cmd/apifixtures -- verify "${verify_args[@]}"

unset LMTOOLS_API_FIXTURE_CASE
unset LMTOOLS_API_FIXTURE_PROVIDER

if [[ -n "$case_id" ]]; then
  export LMTOOLS_API_FIXTURE_CASE="$case_id"
fi

if [[ -n "$provider_id" ]]; then
  export LMTOOLS_API_FIXTURE_PROVIDER="$provider_id"
fi

go test -count=1 ./cmd/apifixtures ./internal/apifixtures ./internal/auth ./internal/core ./internal/proxy -run 'TestVerifySuite|TestAPIFixture|TestCaptureRequestRel|TestLoadCaptureRequestBody|TestEndpointForTarget|TestRefreshDerivedArtifacts|TestCompare|TestProviderSpecStreamingRequestBehavior|TestApplyProviderCredentialsGoogleUsesHeader|TestPrepareRequestPayloadArgoRejectsAudioBlocks|TestTypedToArgoRequestRejectsAudioBlocks|TestDecodeStrictJSONRejectsUnknownField|TestHandleMessagesRejectsUnsupportedMetadataForArgo|TestHandleOpenAIRejectsUnsupportedResponseFormatForArgo'

if (( refresh )); then
  if ! git diff --quiet -- testdata/api-fixtures || [[ -n "$(git status --short --untracked-files=all -- testdata/api-fixtures)" ]]; then
    echo "fixture captures changed; inspect git diff -- testdata/api-fixtures and git status --short -- testdata/api-fixtures" >&2
    git diff --stat -- testdata/api-fixtures || true
    git status --short --untracked-files=all -- testdata/api-fixtures || true
    exit 1
  fi
fi
