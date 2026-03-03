#!/usr/bin/env bash
# Collect and normalize harbor eval output into a structured archive.
#
# Transforms harbor's raw task__hash/ layout into a clean archive with
# deterministic rep numbering, failure categorization, and atomic writes.
#
# Usage:
#   collect-run.sh --harbor-dir DIR --archive-dir DIR [--run-id ID] [--dry-run]
#
#   --harbor-dir DIR    Harbor's raw job output directory (contains task__hash/ dirs)
#   --archive-dir DIR   Target archive run directory (e.g., /data/agent-evals/runs/2026-02-28T...)
#   --run-id ID         Run ID for logging (default: basename of archive-dir)
#   --dry-run           Print what would be done without writing
#
# Idempotent: if archive-dir already exists, skips with a message.
# Atomic: writes to staging dir, then renames on success.
set -euo pipefail

# --- Argument parsing ---

HARBOR_DIR=""
ARCHIVE_DIR=""
RUN_ID=""
DRY_RUN=0

while [[ $# -gt 0 ]]; do
    case "$1" in
        --harbor-dir)  HARBOR_DIR="$2"; shift 2 ;;
        --archive-dir) ARCHIVE_DIR="$2"; shift 2 ;;
        --run-id)      RUN_ID="$2"; shift 2 ;;
        --dry-run)     DRY_RUN=1; shift ;;
        *)
            echo "Unknown argument: $1" >&2
            echo "Usage: collect-run.sh --harbor-dir DIR --archive-dir DIR [--run-id ID] [--dry-run]" >&2
            exit 1
            ;;
    esac
done

if [[ -z "$HARBOR_DIR" ]]; then
    echo "ERROR: --harbor-dir is required" >&2
    exit 1
fi
if [[ -z "$ARCHIVE_DIR" ]]; then
    echo "ERROR: --archive-dir is required" >&2
    exit 1
fi
if [[ -z "$RUN_ID" ]]; then
    RUN_ID="$(basename "$ARCHIVE_DIR")"
fi

if [[ ! -d "$HARBOR_DIR" ]]; then
    echo "ERROR: harbor-dir does not exist: $HARBOR_DIR" >&2
    exit 1
fi

# --- Idempotency check ---

if [[ -d "$ARCHIVE_DIR" ]]; then
    echo "Archive already exists: $ARCHIVE_DIR (skipping)"
    exit 0
fi

STAGING_DIR="${ARCHIVE_DIR}.staging"

# --- Discover task directories ---
# Harbor layout: task-name__hash/ where hash is a short alphanumeric string.
# We write "task_name<TAB>hash" lines to a temp file, sorted by task then hash,
# to avoid bash associative arrays (not available in bash 3.x on macOS).

TASK_LIST_FILE="$(mktemp)"
trap 'rm -f "$TASK_LIST_FILE"' EXIT

for entry in "$HARBOR_DIR"/*/; do
    [[ -d "$entry" ]] || continue
    dirname="$(basename "$entry")"

    # Must match pattern: name__hash
    if [[ "$dirname" != *__* ]]; then
        continue
    fi

    task_name="${dirname%%__*}"
    hash="${dirname##*__}"
    printf '%s\t%s\n' "$task_name" "$hash"
done | sort > "$TASK_LIST_FILE"

total_reps=$(wc -l < "$TASK_LIST_FILE" | tr -d ' ')
if [[ "$total_reps" -eq 0 ]]; then
    echo "ERROR: No task directories found in $HARBOR_DIR (expected task__hash/ pattern)" >&2
    exit 1
fi

# Count unique tasks
task_count=$(cut -f1 "$TASK_LIST_FILE" | sort -u | wc -l | tr -d ' ')

echo "Found $task_count tasks, $total_reps total reps (run: $RUN_ID)"

# --- Assign rep numbers ---
# The file is sorted by task_name then hash, so we assign rep-1, rep-2, ...
# within each task group and build a second file: task_name<TAB>hash<TAB>rep_num

REP_FILE="$(mktemp)"
trap 'rm -f "$TASK_LIST_FILE" "$REP_FILE"' EXIT

prev_task=""
rep_num=0
while IFS=$'\t' read -r task_name hash; do
    if [[ "$task_name" != "$prev_task" ]]; then
        rep_num=1
        prev_task="$task_name"
    else
        rep_num=$((rep_num + 1))
    fi
    printf '%s\t%s\t%d\n' "$task_name" "$hash" "$rep_num"
done < "$TASK_LIST_FILE" > "$REP_FILE"

# --- Dry run: print mapping and exit ---

if [[ "$DRY_RUN" -eq 1 ]]; then
    echo ""
    echo "=== Dry run: mapping ==="
    counter=0
    while IFS=$'\t' read -r task_name hash rep_num; do
        counter=$((counter + 1))
        echo "[$counter/$total_reps] ${task_name} rep-${rep_num} ($hash) -> tasks/${task_name}/rep-${rep_num}/"
    done < "$REP_FILE"
    echo ""
    echo "Would write to: $ARCHIVE_DIR"
    exit 0
fi

# --- Staging setup ---

if [[ -d "$STAGING_DIR" ]]; then
    echo "Removing leftover staging directory: $STAGING_DIR"
    rm -rf "$STAGING_DIR"
fi

mkdir -p "$STAGING_DIR/tasks"

# --- Helper: categorize failure ---

categorize_failure() {
    local harbor_result="$1"
    local agent_stdout="$2"
    local reward="$3"

    # Passing reps have no failure category
    if [[ "$reward" == "1.0" || "$reward" == "1" ]]; then
        echo ""
        return
    fi

    # Check harbor result for timeout
    if [[ -f "$harbor_result" ]] && grep -q "AgentTimeoutError" "$harbor_result"; then
        echo "timeout"
        return
    fi

    # Check agent stdout for submission (wrong answer if submitted but failed)
    if [[ -f "$agent_stdout" ]] && grep -q -E "\[communicate\]|\[submit_result\]" "$agent_stdout"; then
        echo "wrong_answer"
        return
    fi

    # Check agent stdout for API errors
    if [[ -f "$agent_stdout" ]] && grep -q "\[error\]" "$agent_stdout"; then
        echo "api_error"
        return
    fi

    # No submission found
    echo "no_submit"
}

# --- Helper: copy file if source exists ---

copy_if_exists() {
    local src="$1"
    local dst="$2"
    if [[ -f "$src" ]]; then
        cp "$src" "$dst"
    fi
}

# --- Copy files ---

counter=0
while IFS=$'\t' read -r task_name hash rep_num; do
    counter=$((counter + 1))
    harbor_task_dir="$HARBOR_DIR/${task_name}__${hash}"
    rep_dir="$STAGING_DIR/tasks/${task_name}/rep-${rep_num}"

    echo "[$counter/$total_reps] ${task_name} rep-${rep_num} ($hash) -> tasks/${task_name}/rep-${rep_num}/"

    mkdir -p "$rep_dir/sessions"

    # reward.txt
    copy_if_exists "$harbor_task_dir/verifier/reward.txt" "$rep_dir/reward.txt"

    # harbor-result.json (from result.json)
    copy_if_exists "$harbor_task_dir/result.json" "$rep_dir/harbor-result.json"

    # verifier-stdout.txt
    copy_if_exists "$harbor_task_dir/verifier/test-stdout.txt" "$rep_dir/verifier-stdout.txt"

    # agent-stdout.txt
    copy_if_exists "$harbor_task_dir/agent/command-0/stdout.txt" "$rep_dir/agent-stdout.txt"

    # api.jsonl
    copy_if_exists "$harbor_task_dir/agent/serf-state/api.jsonl" "$rep_dir/api.jsonl"

    # sessions/*
    if [[ -d "$harbor_task_dir/agent/serf-state/sessions" ]]; then
        for f in "$harbor_task_dir/agent/serf-state/sessions"/*; do
            [[ -f "$f" ]] && cp "$f" "$rep_dir/sessions/"
        done
    fi

    # artifacts (filtered via rsync)
    if [[ -d "$harbor_task_dir/agent/artifacts" ]]; then
        mkdir -p "$rep_dir/artifacts"
        rsync -a \
            --exclude='.git' \
            --exclude='node_modules' \
            --exclude='__pycache__' \
            --exclude='.venv' \
            --exclude='*.pyc' \
            --exclude='*.o' \
            --exclude='*.so' \
            --exclude='.cache' \
            "$harbor_task_dir/agent/artifacts/" "$rep_dir/artifacts/"
    fi

    # failure_category.txt
    reward=""
    if [[ -f "$rep_dir/reward.txt" ]]; then
        reward="$(head -1 "$rep_dir/reward.txt")"
    fi
    category=$(categorize_failure \
        "$rep_dir/harbor-result.json" \
        "$rep_dir/agent-stdout.txt" \
        "$reward")
    printf '%s\n' "$category" > "$rep_dir/failure_category.txt"
done < "$REP_FILE"

# --- Atomic rename ---

mv "$STAGING_DIR" "$ARCHIVE_DIR"
echo ""
echo "Collected $task_count tasks, $total_reps total reps -> $ARCHIVE_DIR"

# --- Output rep mapping as JSON ---

echo ""
json="{"
prev_task=""
first_task=1
first_rep=1
while IFS=$'\t' read -r task_name hash rep_num; do
    if [[ "$task_name" != "$prev_task" ]]; then
        # Close previous task object (if any)
        if [[ "$first_task" -eq 0 ]]; then
            json+="}"
            json+=", "
        fi
        first_task=0
        json+="\"$task_name\": {"
        first_rep=1
        prev_task="$task_name"
    fi

    if [[ "$first_rep" -eq 0 ]]; then
        json+=", "
    fi
    first_rep=0
    json+="\"rep-${rep_num}\": \"$hash\""
done < "$REP_FILE"

# Close last task object
if [[ "$first_task" -eq 0 ]]; then
    json+="}"
fi
json+="}"
echo "$json"
