#!/usr/bin/env bash
# Re-run the verifier for specific trials without re-running the agent.
#
# Usage:
#   reverify.sh <trial_dir> [trial_dir ...]
#
# Rebuilds the Docker environment from the task's cached Dockerfile,
# mounts the agent's artifacts at /app, uploads /tests, and runs test.sh.
# Overwrites verifier/reward.txt and verifier/test-stdout.txt in-place.
set -euo pipefail

if [[ $# -eq 0 ]]; then
    echo "Usage: reverify.sh <trial_dir> [trial_dir ...]" >&2
    exit 1
fi

TASK_CACHE="$HOME/.cache/harbor/tasks"

for TRIAL_DIR in "$@"; do
    TRIAL_DIR="${TRIAL_DIR%/}"
    TRIAL_NAME="$(basename "$TRIAL_DIR")"

    if [[ ! -d "$TRIAL_DIR" ]]; then
        echo "SKIP $TRIAL_NAME: directory not found" >&2
        continue
    fi

    # Extract task name and find cached task
    TASK_NAME="${TRIAL_NAME%%__*}"
    TASK_DIR=$(find "$TASK_CACHE" -maxdepth 2 -name "$TASK_NAME" -type d 2>/dev/null | head -1)
    if [[ -z "$TASK_DIR" ]]; then
        echo "SKIP $TRIAL_NAME: task '$TASK_NAME' not found in cache" >&2
        continue
    fi

    # Get Docker image from task.toml
    DOCKER_IMAGE=$(grep 'docker_image' "$TASK_DIR/task.toml" | sed 's/.*= *"//;s/".*//')
    if [[ -z "$DOCKER_IMAGE" ]]; then
        echo "SKIP $TRIAL_NAME: no docker_image in task.toml" >&2
        continue
    fi

    TESTS_DIR="$TASK_DIR/tests"
    if [[ ! -d "$TESTS_DIR" ]]; then
        echo "SKIP $TRIAL_NAME: no tests/ directory" >&2
        continue
    fi

    # Find test script (test.sh or whatever the task specifies)
    TEST_SCRIPT="$TESTS_DIR/test.sh"
    if [[ ! -f "$TEST_SCRIPT" ]]; then
        echo "SKIP $TRIAL_NAME: no test.sh found" >&2
        continue
    fi

    ARTIFACTS_DIR="$TRIAL_DIR/agent/artifacts"
    if [[ ! -d "$ARTIFACTS_DIR" ]]; then
        echo "SKIP $TRIAL_NAME: no agent/artifacts directory" >&2
        continue
    fi

    VERIFIER_DIR="$TRIAL_DIR/verifier"
    mkdir -p "$VERIFIER_DIR"

    echo "REVERIFY $TRIAL_NAME (image=$DOCKER_IMAGE)"

    # Clear old verifier results so we don't confuse stale data with new
    rm -f "$VERIFIER_DIR/reward.txt" "$VERIFIER_DIR/test-stdout.txt"

    # Run the verifier in Docker:
    # - Copy artifacts to /app (writable, matching harbor behavior)
    # - Mount tests at /tests (read-only)
    # - Mount verifier output dir at /logs/verifier
    # - Run test.sh via bash (tests mount is read-only, can't chmod)
    docker run --rm \
        -v "$ARTIFACTS_DIR":/app \
        -v "$TESTS_DIR":/tests:ro \
        -v "$VERIFIER_DIR":/logs/verifier \
        -w /app \
        "$DOCKER_IMAGE" \
        bash /tests/test.sh \
        > "$VERIFIER_DIR/test-stdout.txt" 2>&1

    # Report result
    if [[ -f "$VERIFIER_DIR/reward.txt" ]]; then
        REWARD=$(cat "$VERIFIER_DIR/reward.txt")
        if [[ "$REWARD" == "1" || "$REWARD" == "1.0" ]]; then
            echo "  PASS (reward=$REWARD)"
        else
            echo "  FAIL (reward=$REWARD)"
        fi
    else
        echo "  ERROR: no reward.txt produced (check $VERIFIER_DIR/test-stdout.txt)"
    fi
done
