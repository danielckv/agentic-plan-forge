#!/usr/bin/env bash
# Deploy the Plan-Forge relay (plan-forge-relay) to Google Cloud Run as a
# container service. See --help for options.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

NAME_RE='^[a-z][a-z0-9-]{0,48}[a-z0-9]$'
TRUSTED_PROXY_CIDR='169.254.0.0/16'
LISTEN_PORT=8080
CLOUDBUILD_CONFIG='deploy/cloudrun/cloudbuild.yaml'

PROJECT=""
REGION="us-central1"
SERVICE="plan-forge-relay"
REPOSITORY="plan-forge"
SOURCE=""
IMAGE_ARG=""
SECRET="plan-forge-relay-token"
ROTATE_TOKEN=0
MIN_INSTANCES=0
MEMORY="512Mi"
CPU="1"
VERSION=""
DRY_RUN=0
ASSUME_YES=0

# Resolved at run time.
SOURCE_MODE=""   # local | ref | image
SOURCE_REF=""    # git ref (ref mode)
IMAGE_REF=""     # existing image (image mode)
BUILD_DIR=""     # folder uploaded to Cloud Build
BUILD_LABEL=""   # human description of what is built
TMP_DIR=""       # temp export for ref mode
SERVICE_ACCOUNT=""
IMAGE_REPO=""    # REGION-docker.pkg.dev/PROJECT/REPO/SERVICE
IMAGE_TAGGED=""  # IMAGE_REPO:VERSION
DEPLOY_IMAGE=""  # image reference passed to Cloud Run

usage() {
  cat <<'EOF'
Usage: scripts/deploy-relay-cloudrun.sh [options]

Deploy plan-forge-relay to Google Cloud Run as a container service. The image
is built by Cloud Build and deployed pinned by digest.

Options:
  --project ID        GCP project (default: gcloud config get-value project)
  --region REGION     Cloud Run / Artifact Registry region (default: us-central1)
  --service NAME      Cloud Run service name (default: plan-forge-relay)
  --repository NAME   Artifact Registry Docker repository (default: plan-forge)
  --source MODE       What to build and deploy:
                        local         upload this repository's working tree
                        ref:<git-ref> build a clean git archive of a tag,
                                      branch or commit (e.g. ref:v0.1.0)
                        image:<REF>   skip the build; deploy an existing image
                      Without --source, an interactive terminal gets a menu;
                      otherwise (and with --dry-run) the default is local.
  --image REF         Alias for --source image:REF
  --secret NAME       Secret Manager secret holding the relay token
                      (default: plan-forge-relay-token)
  --rotate-token      Add a new random secret version and redeploy
  --min-instances N   Minimum instances (default: 0)
  --memory SIZE       Memory per instance (default: 512Mi)
  --cpu N             vCPUs per instance (default: 1)
  --version V         Image tag and build VERSION (default: the ref name for
                      ref:..., else git describe --tags --always --dirty,
                      else dev)
  --yes               Do not ask for confirmation before building
  --dry-run           Print every gcloud command (prefixed "+ ") instead of
                      running mutating ones; read-only lookups still run
  -h, --help          Show this help

The relay keeps rooms in memory, so the service is pinned to a single instance
(--max-instances 1) and the request timeout is the 60 minute Cloud Run maximum;
clients reconnect automatically after that cut. The service is public at the
Cloud Run layer because the relay enforces its own token and invite checks.
EOF
}

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

info() {
  printf '==> %s\n' "$*"
}

cleanup() {
  if [[ -n ${TMP_DIR} ]]; then
    rm -rf "${TMP_DIR}"
  fi
}
trap cleanup EXIT

# Print a command with a "+ " prefix; run it unless --dry-run.
mut() {
  local line="+"
  local arg
  for arg in "$@"; do
    line+=" $(printf '%q' "${arg}")"
  done
  printf '%s\n' "${line}"
  if [[ ${DRY_RUN} -eq 0 ]]; then
    "$@"
  fi
}

# Like mut, but feeds a freshly generated token to the command on stdin. The
# token is never placed on argv and never printed.
mut_with_token() {
  local line="+ <generated 32-byte token> |"
  local arg
  for arg in "$@"; do
    line+=" $(printf '%q' "${arg}")"
  done
  printf '%s\n' "${line}"
  if [[ ${DRY_RUN} -eq 0 ]]; then
    local token
    token="$(generate_token)"
    printf '%s' "${token}" | "$@"
    token=""
  fi
}

generate_token() {
  local token=""
  if command -v openssl >/dev/null 2>&1; then
    token="$(openssl rand -hex 32)"
  elif command -v xxd >/dev/null 2>&1; then
    token="$(head -c 32 /dev/urandom | xxd -p -c 64)"
  else
    token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
  fi
  [[ ${token} =~ ^[0-9a-f]{64}$ ]] || die "could not generate a 32-byte random token"
  printf '%s' "${token}"
}

# Retry a mutating command: IAM changes on a just-created service account can
# take a few seconds to become visible.
mut_retry() {
  local attempt
  for attempt in 1 2 3 4 5; do
    if mut "$@"; then
      return 0
    fi
    if [[ ${attempt} -lt 5 ]]; then
      info "retrying in 5s (attempt ${attempt}/5)"
      sleep 5
    fi
  done
  return 1
}

is_tty() {
  [[ -t 0 ]]
}

need_value() {
  [[ $# -ge 2 && -n "$2" ]] || die "$1 requires a value"
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --project=* | --region=* | --service=* | --repository=* | --image=* | \
        --source=* | --secret=* | --min-instances=* | --memory=* | --cpu=* | \
        --version=*)
        set -- "${1%%=*}" "${1#*=}" "${@:2}"
        continue
        ;;
    esac
    case "$1" in
      --project) need_value "$@"; PROJECT="$2"; shift 2 ;;
      --region) need_value "$@"; REGION="$2"; shift 2 ;;
      --service) need_value "$@"; SERVICE="$2"; shift 2 ;;
      --repository) need_value "$@"; REPOSITORY="$2"; shift 2 ;;
      --source) need_value "$@"; SOURCE="$2"; shift 2 ;;
      --image) need_value "$@"; IMAGE_ARG="$2"; shift 2 ;;
      --secret) need_value "$@"; SECRET="$2"; shift 2 ;;
      --min-instances) need_value "$@"; MIN_INSTANCES="$2"; shift 2 ;;
      --memory) need_value "$@"; MEMORY="$2"; shift 2 ;;
      --cpu) need_value "$@"; CPU="$2"; shift 2 ;;
      --version) need_value "$@"; VERSION="$2"; shift 2 ;;
      --rotate-token) ROTATE_TOKEN=1; shift ;;
      --yes | -y) ASSUME_YES=1; shift ;;
      --dry-run) DRY_RUN=1; shift ;;
      -h | --help) usage; exit 0 ;;
      *) usage >&2; die "unknown option: $1" ;;
    esac
  done
}

# Sets SOURCE_MODE/SOURCE_REF/IMAGE_REF from SOURCE when it is non-empty.
parse_source() {
  case "${SOURCE}" in
    local)
      SOURCE_MODE="local"
      ;;
    ref:*)
      SOURCE_MODE="ref"
      SOURCE_REF="${SOURCE#ref:}"
      [[ -n ${SOURCE_REF} && ${SOURCE_REF} != -* ]] ||
        die "invalid --source '${SOURCE}': ref:<git-ref> needs a ref that does not start with '-'"
      ;;
    image:*)
      SOURCE_MODE="image"
      IMAGE_REF="${SOURCE#image:}"
      [[ -n ${IMAGE_REF} && ! ${IMAGE_REF} =~ [[:space:]] ]] ||
        die "invalid --source '${SOURCE}': image:<REF> needs a reference without whitespace"
      ;;
    *)
      die "invalid --source '${SOURCE}': expected local, ref:<git-ref> or image:<REF>"
      ;;
  esac
}

validate_inputs() {
  local label value
  for label in service repository secret; do
    case "${label}" in
      service) value="${SERVICE}" ;;
      repository) value="${REPOSITORY}" ;;
      secret) value="${SECRET}" ;;
    esac
    [[ ${value} =~ ${NAME_RE} ]] ||
      die "invalid ${label} name '${value}': must match ${NAME_RE}"
  done
  [[ ${MIN_INSTANCES} =~ ^[0-9]+$ ]] ||
    die "--min-instances must be a non-negative integer, got '${MIN_INSTANCES}'"
  [[ ${REGION} =~ ^[a-z]+-[a-z]+[0-9]+$ ]] ||
    die "invalid region '${REGION}'"
  [[ ${MEMORY} =~ ^[0-9]+(Mi|Gi|M|G)$ ]] ||
    die "invalid --memory '${MEMORY}' (examples: 512Mi, 1Gi)"
  [[ ${CPU} =~ ^[0-9]+(\.[0-9]+)?$ ]] ||
    die "invalid --cpu '${CPU}' (examples: 1, 2)"
  if [[ -n ${IMAGE_ARG} ]]; then
    [[ -z ${SOURCE} ]] || die "--image and --source are mutually exclusive"
    SOURCE="image:${IMAGE_ARG}"
  fi
  if [[ -n ${SOURCE} ]]; then
    parse_source
  fi
}

preflight() {
  command -v gcloud >/dev/null 2>&1 ||
    die "gcloud not found on PATH; install the Google Cloud CLI: https://cloud.google.com/sdk/docs/install"
  local account
  account="$(gcloud auth list --filter=status:ACTIVE --format='value(account)' 2>/dev/null || true)"
  [[ -n ${account} ]] ||
    die "no active gcloud account; run: gcloud auth login"
  if [[ -z ${PROJECT} ]]; then
    PROJECT="$(gcloud config get-value project 2>/dev/null || true)"
  fi
  [[ -n ${PROJECT} && ${PROJECT} != "(unset)" ]] ||
    die "no project set; pass --project ID or run: gcloud config set project ID"
  [[ ${PROJECT} =~ ^[a-z0-9][a-z0-9:.-]*$ ]] ||
    die "invalid project id '${PROJECT}'"
  info "account: ${account}"
}

# Interactive build-source menu; sets SOURCE.
choose_source() {
  local choice="" tag="" pick="" i
  local -a tags=()
  printf 'Select build source:\n' >&2
  printf '  1) local working tree (%s)\n' "${ROOT}" >&2
  printf '  2) git tag or branch\n' >&2
  printf '  3) existing image\n' >&2
  printf 'Choice [1]: ' >&2
  read -r choice || choice=""
  case "${choice:-1}" in
    1)
      SOURCE="local"
      if [[ -n "$(git -C "${ROOT}" status --porcelain 2>/dev/null || true)" ]]; then
        printf 'warning: the working tree has uncommitted changes; they will be uploaded\n' >&2
      fi
      ;;
    2)
      while IFS= read -r tag; do
        [[ -n ${tag} ]] && tags+=("${tag}")
      done < <(git -C "${ROOT}" tag --sort=-creatordate 2>/dev/null | head -n 10)
      if [[ ${#tags[@]} -gt 0 ]]; then
        printf 'Latest tags:\n' >&2
        for i in "${!tags[@]}"; do
          printf '  %d) %s\n' "$((i + 1))" "${tags[${i}]}" >&2
        done
      else
        printf 'No tags found.\n' >&2
      fi
      printf 'Number, or a tag/branch/commit to build: ' >&2
      read -r pick || pick=""
      [[ -n ${pick} ]] || die "no ref chosen"
      if [[ ${pick} =~ ^[0-9]+$ && ${pick} -ge 1 && ${pick} -le ${#tags[@]} ]]; then
        pick="${tags[$((pick - 1))]}"
      fi
      SOURCE="ref:${pick}"
      ;;
    3)
      printf 'Image reference (e.g. REGION-docker.pkg.dev/PROJECT/REPO/IMAGE@sha256:...): ' >&2
      read -r pick || pick=""
      [[ -n ${pick} ]] || die "no image chosen"
      SOURCE="image:${pick}"
      ;;
    *)
      die "invalid choice '${choice}'"
      ;;
  esac
  parse_source
}

resolve_source() {
  if [[ -z ${SOURCE} ]]; then
    if is_tty && [[ ${DRY_RUN} -eq 0 ]]; then
      choose_source
    else
      SOURCE="local"
      parse_source
    fi
  fi
}

resolve_version() {
  if [[ -z ${VERSION} ]]; then
    if [[ ${SOURCE_MODE} == "ref" ]]; then
      VERSION="${SOURCE_REF}"
    else
      VERSION="$(git -C "${ROOT}" describe --tags --always --dirty 2>/dev/null || true)"
    fi
    [[ -n ${VERSION} ]] || VERSION="dev"
  fi
  # Image tags allow [A-Za-z0-9_.-] only and must not start with '.' or '-'.
  VERSION="$(printf '%s' "${VERSION}" | tr -c 'A-Za-z0-9_.-' '-')"
  VERSION="${VERSION:0:128}"
  while [[ ${VERSION} == [.-]* ]]; do
    VERSION="${VERSION:1}"
  done
  [[ -n ${VERSION} ]] || VERSION="dev"
}

# Fail unless DIR looks like a Plan-Forge relay build context.
verify_build_dir() {
  local dir="$1" item
  for item in Dockerfile.relay go.mod; do
    [[ -f ${dir}/${item} ]] ||
      die "build folder ${dir} has no ${item}; it is not a Plan-Forge source tree"
  done
  [[ -d ${dir}/cmd/relay ]] ||
    die "build folder ${dir} has no cmd/relay/; it is not a Plan-Forge source tree"
}

prepare_build_dir() {
  case "${SOURCE_MODE}" in
    local)
      BUILD_DIR="${ROOT}"
      BUILD_LABEL="working tree (local)"
      if [[ -n "$(git -C "${ROOT}" status --porcelain 2>/dev/null || true)" ]]; then
        printf 'warning: the working tree has uncommitted changes; they will be uploaded\n' >&2
      fi
      ;;
    ref)
      local commit
      command -v git >/dev/null 2>&1 || die "git is required for --source ref:"
      commit="$(git -C "${ROOT}" rev-parse --verify "${SOURCE_REF}^{commit}" 2>/dev/null)" ||
        die "git ref '${SOURCE_REF}' does not resolve to a commit in ${ROOT}"
      TMP_DIR="$(mktemp -d "${TMPDIR:-/tmp}/plan-forge-relay-src.XXXXXX")"
      info "exporting ${SOURCE_REF} (${commit:0:12}) to ${TMP_DIR}"
      git -C "${ROOT}" archive "${commit}" | tar -x -C "${TMP_DIR}"
      BUILD_DIR="${TMP_DIR}"
      BUILD_LABEL="git ref ${SOURCE_REF} (${commit:0:12})"
      ;;
  esac
  verify_build_dir "${BUILD_DIR}"
  if [[ ! -f ${BUILD_DIR}/${CLOUDBUILD_CONFIG} ]]; then
    if [[ ${SOURCE_MODE} == "ref" && -f ${ROOT}/${CLOUDBUILD_CONFIG} ]]; then
      info "${SOURCE_REF} has no ${CLOUDBUILD_CONFIG}; using the one from this checkout"
      mkdir -p "$(dirname "${BUILD_DIR}/${CLOUDBUILD_CONFIG}")"
      cp "${ROOT}/${CLOUDBUILD_CONFIG}" "${BUILD_DIR}/${CLOUDBUILD_CONFIG}"
    else
      die "build folder ${BUILD_DIR} has no ${CLOUDBUILD_CONFIG}"
    fi
  fi
}

confirm_build() {
  printf '\nBuild source: %s\n' "${BUILD_LABEL}"
  printf 'Build folder: %s\n' "${BUILD_DIR}"
  printf 'Image:        %s\n\n' "${IMAGE_TAGGED}"
  if [[ ${ASSUME_YES} -eq 0 && ${DRY_RUN} -eq 0 ]] && is_tty; then
    local answer=""
    printf 'Submit this folder to Cloud Build? [y/N] ' >&2
    read -r answer || answer=""
    [[ ${answer} == [yY] || ${answer} == [yY][eE][sS] ]] || die "aborted"
  fi
}

enable_apis() {
  info "enabling required APIs"
  mut gcloud services enable \
    run.googleapis.com \
    artifactregistry.googleapis.com \
    cloudbuild.googleapis.com \
    secretmanager.googleapis.com \
    --project "${PROJECT}"
}

ensure_repository() {
  if gcloud artifacts repositories describe "${REPOSITORY}" \
    --location "${REGION}" --project "${PROJECT}" >/dev/null 2>&1; then
    info "Artifact Registry repository ${REPOSITORY} exists"
    return
  fi
  info "creating Artifact Registry repository ${REPOSITORY}"
  mut gcloud artifacts repositories create "${REPOSITORY}" \
    --repository-format=docker \
    --location "${REGION}" \
    --project "${PROJECT}" \
    --description "Plan-Forge relay images"
}

build_image() {
  info "building ${IMAGE_TAGGED} with Cloud Build from ${BUILD_DIR}"
  cd "${BUILD_DIR}"
  mut gcloud builds submit \
    --config "${CLOUDBUILD_CONFIG}" \
    --substitutions "_IMAGE=${IMAGE_TAGGED},_VERSION=${VERSION}" \
    --project "${PROJECT}" \
    .
  cd "${ROOT}"
}

# Pin Cloud Run to the exact image Cloud Build just pushed.
resolve_digest() {
  if [[ ${DRY_RUN} -eq 1 ]]; then
    DEPLOY_IMAGE="${IMAGE_REPO}@<digest-from-build>"
    return
  fi
  local digest
  digest="$(gcloud artifacts docker images describe "${IMAGE_TAGGED}" \
    --project "${PROJECT}" \
    --format='value(image_summary.digest)' 2>/dev/null || true)"
  [[ -n ${digest} ]] ||
    die "could not resolve the digest of ${IMAGE_TAGGED}; refusing to deploy a mutable tag"
  [[ ${digest} =~ ^sha256:[0-9a-f]{64}$ ]] ||
    die "unexpected image digest '${digest}' for ${IMAGE_TAGGED}"
  DEPLOY_IMAGE="${IMAGE_REPO}@${digest}"
}

ensure_secret() {
  if gcloud secrets describe "${SECRET}" --project "${PROJECT}" >/dev/null 2>&1; then
    if [[ ${ROTATE_TOKEN} -eq 1 ]]; then
      info "adding a new version of secret ${SECRET}"
      mut_with_token gcloud secrets versions add "${SECRET}" \
        --project "${PROJECT}" --data-file=-
    else
      info "secret ${SECRET} exists; keeping its current token"
    fi
    return
  fi
  info "creating secret ${SECRET} with a new random token"
  mut_with_token gcloud secrets create "${SECRET}" \
    --project "${PROJECT}" \
    --replication-policy automatic \
    --data-file=-
}

ensure_service_account() {
  if gcloud iam service-accounts describe "${SERVICE_ACCOUNT}" \
    --project "${PROJECT}" >/dev/null 2>&1; then
    info "service account ${SERVICE_ACCOUNT} exists"
  else
    info "creating service account ${SERVICE_ACCOUNT}"
    mut gcloud iam service-accounts create "${SERVICE}" \
      --display-name "Plan-Forge relay" \
      --project "${PROJECT}"
  fi
  info "granting ${SERVICE_ACCOUNT} access to secret ${SECRET} only"
  mut_retry gcloud secrets add-iam-policy-binding "${SECRET}" \
    --project "${PROJECT}" \
    --member "serviceAccount:${SERVICE_ACCOUNT}" \
    --role roles/secretmanager.secretAccessor \
    --condition=None \
    --quiet
}

deploy_service() {
  info "deploying ${SERVICE} to Cloud Run (${REGION})"
  printf 'Deploying image: %s\n' "${DEPLOY_IMAGE}"
  mut gcloud run deploy "${SERVICE}" \
    --image "${DEPLOY_IMAGE}" \
    --region "${REGION}" \
    --project "${PROJECT}" \
    --service-account "${SERVICE_ACCOUNT}" \
    --port "${LISTEN_PORT}" \
    "--args=--behind-proxy,--trusted-proxy=${TRUSTED_PROXY_CIDR}" \
    "--set-secrets=PLAN_FORGE_RELAY_TOKEN=${SECRET}:latest" \
    --max-instances 1 \
    --min-instances "${MIN_INSTANCES}" \
    --concurrency 1000 \
    --timeout 3600 \
    --no-cpu-throttling \
    --cpu "${CPU}" \
    --memory "${MEMORY}" \
    --allow-unauthenticated \
    --quiet
}

print_summary() {
  local url=""
  url="$(gcloud run services describe "${SERVICE}" \
    --region "${REGION}" --project "${PROJECT}" \
    --format='value(status.url)' 2>/dev/null || true)"
  if [[ -z ${url} ]]; then
    if [[ ${DRY_RUN} -eq 1 ]]; then
      url="https://${SERVICE}-<hash>.${REGION}.run.app"
    else
      die "deploy finished but the service URL could not be read"
    fi
  fi
  cat <<EOF

Relay URL: ${url}

Check it:
  curl -fsS ${url}/healthz

Next steps (on each machine running plan-forge):
  plan-forge config --relay-url ${url}
  gcloud secrets versions access latest --secret ${SECRET} --project ${PROJECT}
  plan-forge config --set-relay-token     # paste the token at the hidden prompt

Teammates need the same relay URL and token. Rooms live in memory; a restart
or the 60 minute connection cut is recovered by the client reconnecting.
EOF
}

main() {
  parse_args "$@"
  validate_inputs
  cd "${ROOT}"
  preflight
  resolve_source
  resolve_version
  SERVICE_ACCOUNT="${SERVICE}@${PROJECT}.iam.gserviceaccount.com"
  IMAGE_REPO="${REGION}-docker.pkg.dev/${PROJECT}/${REPOSITORY}/${SERVICE}"
  IMAGE_TAGGED="${IMAGE_REPO}:${VERSION}"

  if [[ ${DRY_RUN} -eq 1 ]]; then
    info "dry run: mutating gcloud commands are printed, not executed"
  fi

  if [[ ${SOURCE_MODE} != "image" ]]; then
    prepare_build_dir
    confirm_build
  else
    info "using existing image ${IMAGE_REF}; skipping build"
  fi

  enable_apis
  if [[ ${SOURCE_MODE} != "image" ]]; then
    ensure_repository
    build_image
    resolve_digest
  else
    DEPLOY_IMAGE="${IMAGE_REF}"
  fi
  ensure_secret
  ensure_service_account
  deploy_service
  print_summary
}

main "$@"
