#!/usr/bin/env bash
# Tests for scripts/deploy-relay-cloudrun.sh using a fake gcloud on PATH.
# Nothing here talks to Google Cloud.
set -euo pipefail

TEST_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${TEST_DIR}/../.." && pwd -P)"
SCRIPT="${ROOT}/scripts/deploy-relay-cloudrun.sh"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/pf-cloudrun-test.XXXXXX")"
trap 'rm -rf "${WORK}"' EXIT

FAKE_BIN="${WORK}/bin"
GCLOUD_LOG="${WORK}/gcloud.log"
export GCLOUD_LOG
mkdir -p "${FAKE_BIN}"

cat >"${FAKE_BIN}/gcloud" <<'FAKE'
#!/usr/bin/env bash
printf 'PWD=%s %s\n' "${PWD}" "$*" >>"${GCLOUD_LOG}"
args=" $* "
case "${args}" in
  *" --data-file=- "*) cat >/dev/null ;;
esac
case "${args}" in
  *" config get-value project "*) echo "test-proj" ;;
  *" auth list "*) echo "tester@example.com" ;;
  *" artifacts docker images describe "*)
    printf 'sha256:%064d\n' 0 | tr 0 a
    ;;
  *" run services describe "*) echo "https://plan-forge-relay-test.a.run.app" ;;
  *" describe "*) exit 1 ;;
esac
exit 0
FAKE
chmod +x "${FAKE_BIN}/gcloud"

FAILURES=0
OUT=""
RC=0

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  FAILURES=$((FAILURES + 1))
}

pass() {
  printf 'ok:   %s\n' "$*"
}

# run_script ARGS...: sets OUT (stdout+stderr) and RC; resets the gcloud log.
run_script() {
  : >"${GCLOUD_LOG}"
  set +e
  OUT="$(PATH="${FAKE_BIN}:${PATH}" bash "${SCRIPT}" "$@" </dev/null 2>&1)"
  RC=$?
  set -e
}

log_text() {
  cat "${GCLOUD_LOG}"
}

# expect_contains NAME HAYSTACK NEEDLE
expect_contains() {
  if [[ $2 != *"$3"* ]]; then
    fail "$1: expected to find '$3'"
    return 1
  fi
}

# expect_absent NAME HAYSTACK NEEDLE
expect_absent() {
  if [[ $2 == *"$3"* ]]; then
    fail "$1: did not expect '$3'"
    return 1
  fi
}

expect_rc_ok() {
  if [[ ${RC} -ne 0 ]]; then
    fail "$1: exit ${RC}, want 0; output: ${OUT}"
    return 1
  fi
}

expect_rc_fail() {
  if [[ ${RC} -eq 0 ]]; then
    fail "$1: exit 0, want non-zero; output: ${OUT}"
    return 1
  fi
}

case_dry_run_default() {
  local name="dry-run default"
  run_script --dry-run
  expect_rc_ok "${name}" || return 0
  local needle
  for needle in \
    'builds submit' \
    'run deploy' \
    '--max-instances 1' \
    '--trusted-proxy=169.254.0.0/16' \
    'PLAN_FORGE_RELAY_TOKEN=plan-forge-relay-token:latest' \
    '--no-cpu-throttling' \
    'us-central1-docker.pkg.dev/test-proj/plan-forge/plan-forge-relay@' \
    'Deploying image: ' \
    '<digest-from-build>'; do
    expect_contains "${name}" "${OUT}" "${needle}" || return 0
  done
  # Mutating commands must not have reached gcloud.
  expect_absent "${name} (log)" "$(log_text)" 'run deploy' || return 0
  expect_absent "${name} (log)" "$(log_text)" 'builds submit' || return 0
  pass "${name}"
}

case_image_skips_build() {
  local name="--image skips build"
  run_script --dry-run --image us-docker.pkg.dev/p/r/relay:1
  expect_rc_ok "${name}" || return 0
  expect_absent "${name}" "${OUT}" 'builds submit' || return 0
  expect_absent "${name}" "${OUT}" 'artifacts repositories create' || return 0
  expect_contains "${name}" "${OUT}" 'run deploy' || return 0
  expect_contains "${name}" "${OUT}" 'us-docker.pkg.dev/p/r/relay:1' || return 0
  pass "${name}"
}

case_source_image() {
  local name="--source image:X skips build"
  run_script --source image:us-docker.pkg.dev/p/r/relay@sha256:abc --yes
  expect_rc_ok "${name}" || return 0
  local log
  log="$(log_text)"
  expect_absent "${name}" "${log}" 'builds submit' || return 0
  expect_absent "${name}" "${log}" 'artifacts repositories create' || return 0
  expect_contains "${name}" "${log}" 'run deploy' || return 0
  expect_contains "${name}" "${log}" '--image us-docker.pkg.dev/p/r/relay@sha256:abc' || return 0
  pass "${name}"
}

case_real_run_secret_and_digest() {
  local name="real run creates secret, pins digest, hides token"
  run_script --source local --yes
  expect_rc_ok "${name}" || return 0
  local log
  log="$(log_text)"
  expect_contains "${name}" "${log}" 'secrets create plan-forge-relay-token' || return 0
  expect_contains "${name}" "${log}" '--data-file=-' || return 0
  expect_contains "${name}" "${log}" 'builds submit' || return 0
  expect_contains "${name}" "${log}" 'artifacts repositories create' || return 0
  expect_contains "${name}" "${log}" 'artifacts docker images describe' || return 0
  expect_contains "${name}" "${log}" 'secretmanager.secretAccessor' || return 0
  local deploy_line
  deploy_line="$(grep 'run deploy' "${GCLOUD_LOG}" || true)"
  expect_contains "${name}" "${deploy_line}" '@sha256:' || return 0
  expect_absent "${name}" "${deploy_line}" 'plan-forge-relay:' || return 0
  expect_contains "${name}" "${OUT}" 'Deploying image: us-central1-docker.pkg.dev/test-proj/plan-forge/plan-forge-relay@sha256:' || return 0
  expect_contains "${name}" "${OUT}" 'https://plan-forge-relay-test.a.run.app' || return 0
  # The only 64-hex strings allowed in argv are image digests, never a token.
  if sed -E 's/sha256:[0-9a-f]{64}//g' "${GCLOUD_LOG}" | grep -Eq '[0-9a-f]{64}'; then
    fail "${name}: a 64-hex string (token?) appears in gcloud argv"
    return 0
  fi
  if printf '%s' "${OUT}" | sed -E 's/sha256:[0-9a-f]{64}//g' | grep -Eq '[0-9a-f]{64}'; then
    fail "${name}: a 64-hex string (token?) appears in script output"
    return 0
  fi
  pass "${name}"
}

case_local_builds_from_root() {
  local name="--source local builds from repo root"
  run_script --source local --yes
  expect_rc_ok "${name}" || return 0
  local dir
  dir="$(grep 'builds submit' "${GCLOUD_LOG}" | sed -E 's/^PWD=([^ ]*) .*/\1/')"
  local real
  real="$(cd "${dir}" && pwd -P)"
  if [[ ${real} != "${ROOT}" ]]; then
    fail "${name}: builds submit ran in ${real}, want ${ROOT}"
    return 0
  fi
  pass "${name}"
}

case_ref_builds_from_temp_dir() {
  local name="--source ref:HEAD builds from a temp dir"
  run_script --source ref:HEAD --yes
  expect_rc_ok "${name}" || return 0
  local line dir
  line="$(grep 'builds submit' "${GCLOUD_LOG}" || true)"
  if [[ -z ${line} ]]; then
    fail "${name}: no builds submit recorded"
    return 0
  fi
  dir="$(printf '%s' "${line}" | sed -E 's/^PWD=([^ ]*) .*/\1/')"
  if [[ $(cd "${dir}" 2>/dev/null && pwd -P || true) == "${ROOT}" ]]; then
    fail "${name}: builds submit ran from the repo root"
    return 0
  fi
  expect_contains "${name}" "${dir}" 'plan-forge-relay-src.' || return 0
  expect_contains "${name}" "${line}" '--config deploy/cloudrun/cloudbuild.yaml' || return 0
  expect_contains "${name}" "${OUT}" 'Build folder: ' || return 0
  expect_contains "${name}" "${OUT}" 'git ref HEAD' || return 0
  if [[ -e ${dir} ]]; then
    fail "${name}: temp folder ${dir} was not cleaned up"
    return 0
  fi
  pass "${name}"
}

case_ref_invalid() {
  local name="invalid ref exits non-zero"
  run_script --source ref:no-such-ref --yes
  expect_rc_fail "${name}" || return 0
  expect_contains "${name}" "${OUT}" 'no-such-ref' || return 0
  expect_absent "${name}" "$(log_text)" 'builds submit' || return 0
  pass "${name}"
}

case_source_bogus() {
  local name="bogus --source exits non-zero"
  run_script --source bogus --dry-run
  expect_rc_fail "${name}" || return 0
  expect_contains "${name}" "${OUT}" '--source' || return 0
  pass "${name}"
}

case_bad_min_instances() {
  local name="bad --min-instances exits non-zero"
  run_script --dry-run --min-instances abc
  expect_rc_fail "${name}" || return 0
  expect_contains "${name}" "${OUT}" 'min-instances' || return 0
  pass "${name}"
}

case_bad_service_name() {
  local name="bad service name exits non-zero"
  run_script --dry-run --service 'Bad_Name'
  expect_rc_fail "${name}" || return 0
  expect_contains "${name}" "${OUT}" 'service' || return 0
  pass "${name}"
}

case_dry_run_default
case_image_skips_build
case_source_image
case_real_run_secret_and_digest
case_local_builds_from_root
case_ref_builds_from_temp_dir
case_ref_invalid
case_source_bogus
case_bad_min_instances
case_bad_service_name

if [[ ${FAILURES} -ne 0 ]]; then
  printf '\n%d case(s) failed\n' "${FAILURES}" >&2
  exit 1
fi
printf '\nall cloudrun deploy cases passed\n'
