#!/usr/bin/env bash
#
# agent-entrypoint: clone the target repo at a ref, run Claude Code headless
# against it, and capture a transcript + result.json. This is the contract the
# orchestrator (internal/runtime, issue #17) drives a container with — it speaks
# only through environment variables in and files/stdout out, so the same image
# works under the Docker backend and, later, Cloud Run Jobs.
#
# Inputs (environment):
#   ANTHROPIC_API_KEY      (required) Anthropic API key for Claude Code
#   AGENT_REPO             (required) git URL, or "owner/name" (-> GitHub https)
#   AGENT_PROMPT           task instruction for Claude Code
#   AGENT_PROMPT_FILE      alternative to AGENT_PROMPT: path to a file with the
#                          prompt (preferred for large prompts). One of
#                          AGENT_PROMPT / AGENT_PROMPT_FILE is required.
#   AGENT_REF              branch/tag/commit to check out (default: repo default)
#   AGENT_GIT_TOKEN        token for cloning/pushing private repos (optional)
#   AGENT_GIT_USER_NAME    git author name   (default "Cortex Agent")
#   AGENT_GIT_USER_EMAIL   git author email  (default "agent@cortex.local")
#   CORTEX_CLAUDE_MODEL    model id passed to `claude --model` (optional)
#   CORTEX_SESSION_ID      Cortex session id, recorded in result.json (optional)
#   AGENT_TIMEOUT_SECONDS  hard cap on the Claude Code run (default 1800)
#   AGENT_WORKSPACE        where the repo is cloned (default /workspace/repo)
#   AGENT_OUTPUT_DIR       where transcript.jsonl + result.json are written
#                          (default /workspace/out)
#   AGENT_SKIP_PERMISSIONS truthy => pass --dangerously-skip-permissions so the
#                          agent can use tools unattended in this sandbox
#                          (default true; set 0/false to require approvals)
#
# Outputs:
#   stdout                 the raw Claude Code stream-json transcript (so the
#                          orchestrator can tail container logs live)
#   $AGENT_OUTPUT_DIR/transcript.jsonl   same transcript, persisted
#   $AGENT_OUTPUT_DIR/result.json        machine-readable summary
#   exit code              Claude Code's exit code, or 124 on timeout
#
set -euo pipefail

log() { printf '[agent-entrypoint] %s\n' "$*" >&2; }
fail() { log "ERROR: $*"; exit 64; }

# --- resolve config ----------------------------------------------------------
: "${ANTHROPIC_API_KEY:?ANTHROPIC_API_KEY is required}"
: "${AGENT_REPO:?AGENT_REPO is required}"

WORKSPACE="${AGENT_WORKSPACE:-/workspace/repo}"
OUTPUT_DIR="${AGENT_OUTPUT_DIR:-/workspace/out}"
TIMEOUT_SECONDS="${AGENT_TIMEOUT_SECONDS:-1800}"
GIT_USER_NAME="${AGENT_GIT_USER_NAME:-Cortex Agent}"
GIT_USER_EMAIL="${AGENT_GIT_USER_EMAIL:-agent@cortex.local}"

# Resolve the prompt (inline or file).
if [[ -n "${AGENT_PROMPT_FILE:-}" ]]; then
    [[ -r "${AGENT_PROMPT_FILE}" ]] || fail "AGENT_PROMPT_FILE not readable: ${AGENT_PROMPT_FILE}"
    PROMPT="$(cat "${AGENT_PROMPT_FILE}")"
else
    PROMPT="${AGENT_PROMPT:-}"
fi
[[ -n "${PROMPT}" ]] || fail "one of AGENT_PROMPT or AGENT_PROMPT_FILE is required"

mkdir -p "${OUTPUT_DIR}"
TRANSCRIPT="${OUTPUT_DIR}/transcript.jsonl"
RESULT="${OUTPUT_DIR}/result.json"

# --- normalize the repo URL --------------------------------------------------
# Accept a bare "owner/name" as a GitHub https URL; leave full URLs untouched.
REPO_URL="${AGENT_REPO}"
if [[ "${REPO_URL}" != *://* && "${REPO_URL}" != git@* ]]; then
    REPO_URL="https://github.com/${REPO_URL}.git"
fi

# Inject the token for https clones without ever printing it. Uses a git
# credential helper rather than embedding it in the URL so it stays out of
# `git remote -v`, the reflog, and our logs.
CLONE_ENV=()
if [[ -n "${AGENT_GIT_TOKEN:-}" && "${REPO_URL}" == https://* ]]; then
    # Single quotes are deliberate: this snippet is run by git later, and reads
    # AGENT_GIT_TOKEN from the environment at that time so the token is never
    # written into git config on disk.
    # shellcheck disable=SC2016
    git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=${AGENT_GIT_TOKEN}"; }; f'
    CLONE_ENV=(GIT_TERMINAL_PROMPT=0)
fi

# --- clone + checkout --------------------------------------------------------
log "cloning ${REPO_URL} -> ${WORKSPACE}"
rm -rf "${WORKSPACE}"
env "${CLONE_ENV[@]}" git clone "${REPO_URL}" "${WORKSPACE}"
cd "${WORKSPACE}"

if [[ -n "${AGENT_REF:-}" ]]; then
    log "checking out ref ${AGENT_REF}"
    git fetch --depth 1 origin "${AGENT_REF}" 2>/dev/null || true
    git checkout "${AGENT_REF}"
fi

git config user.name "${GIT_USER_NAME}"
git config user.email "${GIT_USER_EMAIL}"

START_COMMIT="$(git rev-parse HEAD)"
START_TS="$(date -u +%FT%TZ)"
START_EPOCH="$(date +%s)"
log "starting commit ${START_COMMIT} on $(git rev-parse --abbrev-ref HEAD)"

# --- run Claude Code headless ------------------------------------------------
CLAUDE_ARGS=(--print --output-format stream-json --verbose)
case "$(printf '%s' "${AGENT_SKIP_PERMISSIONS:-true}" | tr '[:upper:]' '[:lower:]')" in
    1|true|yes|on) CLAUDE_ARGS+=(--dangerously-skip-permissions) ;;
esac
if [[ -n "${CORTEX_CLAUDE_MODEL:-}" ]]; then
    CLAUDE_ARGS+=(--model "${CORTEX_CLAUDE_MODEL}")
fi

log "running claude (timeout ${TIMEOUT_SECONDS}s): claude ${CLAUDE_ARGS[*]}"
TIMED_OUT=false
EXIT_CODE=0
# `timeout` returns 124 when it kills the run; capture it without tripping -e.
set +e
timeout --signal=TERM --kill-after=10s "${TIMEOUT_SECONDS}" \
    claude "${CLAUDE_ARGS[@]}" "${PROMPT}" 2>>"${OUTPUT_DIR}/claude.stderr" \
    | tee "${TRANSCRIPT}"
EXIT_CODE=${PIPESTATUS[0]}
set -e
if [[ "${EXIT_CODE}" -eq 124 ]]; then
    TIMED_OUT=true
    log "claude run timed out after ${TIMEOUT_SECONDS}s"
fi
log "claude exited ${EXIT_CODE}"

# --- collect results ---------------------------------------------------------
END_TS="$(date -u +%FT%TZ)"
END_EPOCH="$(date +%s)"
HEAD_COMMIT="$(git rev-parse HEAD)"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
CHANGED_FILES="$(git status --porcelain | wc -l | tr -d ' ')"
# Diff of committed work since we started, plus any uncommitted changes.
git diff "${START_COMMIT}" HEAD > "${OUTPUT_DIR}/committed.diff" 2>/dev/null || true
git diff > "${OUTPUT_DIR}/working.diff" 2>/dev/null || true
DIFFSTAT="$(git diff --stat "${START_COMMIT}" HEAD 2>/dev/null | tail -n1 | sed 's/^ *//')"

jq -n \
    --arg schema "1" \
    --arg session "${CORTEX_SESSION_ID:-}" \
    --arg repo "${AGENT_REPO}" \
    --arg ref "${AGENT_REF:-}" \
    --arg model "${CORTEX_CLAUDE_MODEL:-}" \
    --arg start_commit "${START_COMMIT}" \
    --arg head_commit "${HEAD_COMMIT}" \
    --arg branch "${BRANCH}" \
    --argjson exit_code "${EXIT_CODE}" \
    --argjson timed_out "${TIMED_OUT}" \
    --argjson changed_files "${CHANGED_FILES:-0}" \
    --arg diffstat "${DIFFSTAT:-}" \
    --arg started_at "${START_TS}" \
    --arg finished_at "${END_TS}" \
    --argjson duration_seconds "$((END_EPOCH - START_EPOCH))" \
    '{schema_version: $schema, session_id: $session, repo: $repo, ref: $ref,
      model: $model, start_commit: $start_commit, head_commit: $head_commit,
      branch: $branch, exit_code: $exit_code, timed_out: $timed_out,
      changed_files: $changed_files, diffstat: $diffstat,
      started_at: $started_at, finished_at: $finished_at,
      duration_seconds: $duration_seconds}' \
    > "${RESULT}"

log "wrote ${RESULT} (changed_files=${CHANGED_FILES}, head=${HEAD_COMMIT})"

# Surface the run's outcome as the container exit code so the orchestrator can
# distinguish success / failure / timeout without parsing files.
exit "${EXIT_CODE}"
