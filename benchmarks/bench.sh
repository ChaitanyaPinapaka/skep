#!/usr/bin/env bash
# bench.sh — run the skep benchmark matrix.
#
# For each (repo, task) pair, measures three quantities:
#
#   1. Classifier+plan cost (skep-only, flat overhead per task)
#      The classifier is a one-shot shell-out to the classify LLM with a
#      pre-built context from the index. It does NOT use MCP — it's a
#      planner, not an explorer. There is no "baseline" or "with MCP"
#      variant because without skep there is no classifier at all.
#
#   2. Executor cost WITHOUT skep MCP (cold-read baseline)
#      Plain `claude -p "<task>"` with Edit/Write/Bash tools. Claude
#      explores the repo via Read/Grep/Glob as needed.
#
#   3. Executor cost WITH skep MCP (treatment)
#      Same `claude -p "<task>"` but with --mcp-config pointing at
#      skep's stdio MCP server, so Claude can call search_symbols,
#      get_file_context, get_call_graph instead of blindly reading files.
#
# Total cost per task:
#   - Without skep:  (executor_baseline)
#   - With skep:     (classifier + executor_mcp)
# Skep wins when (classifier + executor_mcp) < executor_baseline.
#
# Results land in benchmarks/results/<timestamp>/ as per-run JSON files
# plus an aggregated results.md matrix.
#
# Usage:
#   ./bench.sh <repo-path> [<repo-path>...]
#
# Prereqs:
#   - skep installed on PATH (make install)
#   - claude code CLI on PATH
#   - each target repo already run through `skep init` with a valid llm config
#   - each target repo is on a clean git branch (we branch off per task)
#   - benchmarks/measure built: (cd benchmarks/measure && go build -o ../measure.bin)
#
# This script is deliberately bash + go and does not touch any state
# outside the target repos' own git worktrees (each task runs on a fresh
# throwaway branch that gets deleted after measurement).

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MEASURE_BIN="$HERE/measure.bin"
TASKS_FILE="$HERE/tasks/tasks.yaml"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_DIR="$HERE/results/$TS"
mkdir -p "$OUT_DIR"

if [[ ! -x "$MEASURE_BIN" ]]; then
  echo "Building measure tool..."
  (cd "$HERE/measure" && go build -o "$MEASURE_BIN" .)
fi

if [[ $# -eq 0 ]]; then
  echo "usage: $0 <repo-path> [<repo-path>...]" >&2
  exit 2
fi

if ! command -v claude >/dev/null; then
  echo "claude code CLI not found on PATH" >&2
  exit 1
fi

# -----------------------------------------------------------------------
# Task list extraction (bash-only YAML parse — tolerable for our fixed schema)
# -----------------------------------------------------------------------
TASK_IDS=()
declare -A TASK_DESC
current_id=""
current_desc=""
in_desc=0
while IFS= read -r line; do
  if [[ "$line" =~ ^[[:space:]]*-[[:space:]]*id:[[:space:]]*(.*)$ ]]; then
    if [[ -n "$current_id" ]]; then
      TASK_DESC["$current_id"]="$(echo -n "$current_desc" | sed 's/[[:space:]]*$//')"
    fi
    current_id="${BASH_REMATCH[1]}"
    current_desc=""
    in_desc=0
    TASK_IDS+=("$current_id")
  elif [[ "$line" =~ ^[[:space:]]*description:[[:space:]]*\|[[:space:]]*$ ]]; then
    in_desc=1
  elif [[ $in_desc -eq 1 && "$line" =~ ^[[:space:]]{4,} ]]; then
    current_desc+="${line#    } "
  elif [[ $in_desc -eq 1 && -z "$line" ]]; then
    :
  else
    in_desc=0
  fi
done < "$TASKS_FILE"
if [[ -n "$current_id" ]]; then
  TASK_DESC["$current_id"]="$(echo -n "$current_desc" | sed 's/[[:space:]]*$//')"
fi

# -----------------------------------------------------------------------
# Find the latest Claude session file matching a working directory.
# Claude Code writes sessions under ~/.claude/projects/<encoded-path>/<uuid>.jsonl
# We glob for the newest file after our run.
# -----------------------------------------------------------------------
latest_session() {
  local workdir="$1"
  local after_epoch="$2"
  # Encode the workdir the way Claude Code does — slashes become dashes,
  # leading slash becomes leading dash.
  local encoded
  encoded="$(echo -n "$workdir" | sed 's|/|-|g')"
  local dir="$HOME/.claude/projects/$encoded"
  [[ -d "$dir" ]] || return 1
  # Pick the newest .jsonl modified after we started the run.
  local newest=""
  local newest_mtime=0
  for f in "$dir"/*.jsonl; do
    [[ -f "$f" ]] || continue
    local mtime
    mtime=$(stat -c %Y "$f" 2>/dev/null || stat -f %m "$f" 2>/dev/null || echo 0)
    if [[ "$mtime" -ge "$after_epoch" ]] && [[ "$mtime" -gt "$newest_mtime" ]]; then
      newest="$f"
      newest_mtime="$mtime"
    fi
  done
  [[ -n "$newest" ]] || return 1
  echo "$newest"
}

# -----------------------------------------------------------------------
# Run one variant of one task. Emits a JSON row into $OUT_DIR.
# Args: repo-path, task-id, variant {baseline|mcp}, description
# -----------------------------------------------------------------------
run_variant() {
  local repo="$1"
  local task_id="$2"
  local variant="$3"
  local desc="$4"

  local repo_name
  repo_name="$(basename "$repo")"
  local branch="skep-bench/${task_id}-${variant}-${TS}"
  local session_file=""

  echo "  [$variant] $task_id..."
  pushd "$repo" >/dev/null

  # Fresh branch per run so concurrent runs don't collide and so the repo's
  # main branch stays untouched.
  git checkout -B "$branch" >/dev/null 2>&1

  local start_epoch
  start_epoch=$(date +%s)

  local mcp_flag=""
  if [[ "$variant" == "mcp" ]]; then
    # Point Claude Code at this repo's skep MCP server for the run.
    local mcp_config
    mcp_config="$(mktemp)"
    cat > "$mcp_config" <<EOF
{
  "mcpServers": {
    "skep": { "command": "skep", "args": ["mcp"] }
  }
}
EOF
    mcp_flag="--mcp-config $mcp_config"
  fi

  # One-shot non-interactive Claude run. --dangerously-skip-permissions
  # auto-approves edit prompts so the benchmark actually completes.
  set +e
  claude -p "$desc" \
    $mcp_flag \
    --allowedTools Edit,Write,Bash \
    --dangerously-skip-permissions \
    >/dev/null 2>&1
  local exit_code=$?
  set -e

  # Locate the session JSONL that Claude just wrote.
  session_file="$(latest_session "$repo" "$start_epoch" || echo "")"

  # Diff the branch against its parent to record what changed.
  local diff_lines=0
  if git rev-parse --verify HEAD >/dev/null 2>&1; then
    diff_lines=$(git diff HEAD~1 --shortstat 2>/dev/null | awk '{print $4+$6}' || echo 0)
  fi

  popd >/dev/null

  # Measure the session.
  local usage_json="{}"
  if [[ -n "$session_file" ]]; then
    usage_json="$("$MEASURE_BIN" "$session_file" 2>/dev/null || echo '{}')"
  fi

  # Emit a row into the result directory.
  local row_file="$OUT_DIR/${repo_name}_${task_id}_${variant}.json"
  cat > "$row_file" <<EOF
{
  "repo": "$repo_name",
  "task_id": "$task_id",
  "variant": "$variant",
  "exit_code": $exit_code,
  "diff_lines": $diff_lines,
  "session_file": "$session_file",
  "usage": $usage_json
}
EOF

  # Clean up the bench branch.
  (cd "$repo" && git checkout -q - >/dev/null 2>&1 && git branch -D "$branch" >/dev/null 2>&1) || true
}

# -----------------------------------------------------------------------
# Classifier-only measurement. Runs `skep task create <desc>` which
# invokes the classify LLM call, then measures the task's resulting session
# if any — but classify uses its own shell-out rather than a Claude session,
# so we can't directly measure it via the session JSONL. We record wall
# clock and approximate cost via the configured classify model's token
# usage estimate from the task row (skep writes tokens_used into the
# tasks table as part of Phase 3, not yet wired). For now, stamp a
# placeholder "classify_wall_clock_sec" and move on.
# -----------------------------------------------------------------------
run_classify() {
  local repo="$1"
  local task_id="$2"
  local desc="$3"
  local repo_name
  repo_name="$(basename "$repo")"

  echo "  [classify] $task_id..."
  pushd "$repo" >/dev/null

  local start_epoch
  start_epoch=$(date +%s)

  set +e
  skep task create "$desc" --json 2>/dev/null > "$OUT_DIR/${repo_name}_${task_id}_classify.json.raw"
  local exit_code=$?
  set -e

  local wall
  wall=$(( $(date +%s) - start_epoch ))

  # Delete the synthetic classifier task so it doesn't pile up in the queue.
  local task_id_real
  task_id_real=$(grep -o '"id":[0-9]*' "$OUT_DIR/${repo_name}_${task_id}_classify.json.raw" | head -1 | cut -d: -f2 || echo "")
  if [[ -n "$task_id_real" ]]; then
    skep task delete "$task_id_real" --yes >/dev/null 2>&1 || true
  fi

  popd >/dev/null

  local row_file="$OUT_DIR/${repo_name}_${task_id}_classify.json"
  cat > "$row_file" <<EOF
{
  "repo": "$repo_name",
  "task_id": "$task_id",
  "variant": "classify",
  "exit_code": $exit_code,
  "wall_clock_sec": $wall
}
EOF
}

# -----------------------------------------------------------------------
# Main loop
# -----------------------------------------------------------------------
for repo in "$@"; do
  if [[ ! -d "$repo/.skep" ]]; then
    echo "skip $repo: not initialized (run 'skep init' first)" >&2
    continue
  fi
  echo "== $(basename "$repo") =="
  for task_id in "${TASK_IDS[@]}"; do
    desc="${TASK_DESC[$task_id]}"
    run_classify "$repo" "$task_id" "$desc"
    run_variant "$repo" "$task_id" "baseline" "$desc"
    run_variant "$repo" "$task_id" "mcp" "$desc"
  done
done

echo
echo "Results: $OUT_DIR"
echo "To aggregate into a markdown table:"
echo "  ./benchmarks/aggregate.sh $OUT_DIR > $OUT_DIR/results.md"
