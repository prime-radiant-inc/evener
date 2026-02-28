#!/usr/bin/env bash
# Check status and results of a running or completed eval job.
#
# Usage:
#   ./tools/check-eval.sh <job-name>
#
# Example:
#   ./tools/check-eval.sh reviewer-v2
set -euo pipefail

REMOTE=jesse@192.168.118.101
JOB_NAME="${1:?Usage: check-eval.sh <job-name>}"

ssh "$REMOTE" bash <<REMOTE
echo "=== Process ==="
ps aux | grep "job-name $JOB_NAME" | grep -v grep || echo "(not running)"

echo ""
echo "=== Results ==="
pass=0; fail=0; pending=0

# Harbor puts results in several possible locations:
#   /tmp/<job>/<job>/<task>__<hash>/   (--jobs-dir /tmp/<job>)
#   /tmp/<job>/<task>__<hash>/         (flat --jobs-dir)
#   jobs/<job>/<task>__<hash>/         (default cwd/jobs/)
RESULTS_DIR=""
for candidate in "/tmp/$JOB_NAME/$JOB_NAME" "/tmp/$JOB_NAME" "$HOME/$REMOTE_DIR/jobs/$JOB_NAME"; do
    if [ -d "\$candidate" ]; then
        # Check if it has task subdirectories (not just job.log)
        if ls "\$candidate"/*/verifier/reward.txt >/dev/null 2>&1 || \
           ls "\$candidate"/*/reward.txt >/dev/null 2>&1 || \
           ls "\$candidate"/*/result.json >/dev/null 2>&1; then
            RESULTS_DIR="\$candidate"
            break
        fi
    fi
done

if [ -z "\$RESULTS_DIR" ]; then
    # Fall back to /tmp/<job> even without results (might still be running)
    RESULTS_DIR="/tmp/$JOB_NAME"
    if [ ! -d "\$RESULTS_DIR" ]; then
        echo "  No results directory found for $JOB_NAME"
        echo "  Checked: /tmp/$JOB_NAME, /tmp/$JOB_NAME/$JOB_NAME, cwd/jobs/$JOB_NAME"
        exit 0
    fi
fi

echo "(results in \$RESULTS_DIR)"
echo ""

for d in \$RESULTS_DIR/*/; do
    [ -d "\$d" ] || continue
    task=\$(basename "\$d" | sed 's/__.*$//')
    # Skip non-task dirs (like "config.json" dirs shouldn't match but be safe)
    [ -f "\$d/result.json" ] || [ -f "\$d/verifier/reward.txt" ] || [ -f "\$d/reward.txt" ] || continue
    reward=\$(cat "\$d/verifier/reward.txt" "\$d/reward.txt" 2>/dev/null | head -1)
    if [ -z "\$reward" ]; then
        echo "  \$task: RUNNING"
        pending=\$((pending+1))
    elif [ "\$reward" = "1.0" ] || [ "\$reward" = "1" ]; then
        echo "  \$task: PASS"
        pass=\$((pass+1))
    else
        echo "  \$task: FAIL (\$reward)"
        fail=\$((fail+1))
    fi
done
total=\$((pass+fail+pending))
echo ""
echo "=== Summary: \$pass/\$total pass, \$fail fail, \$pending running ==="

echo ""
echo "=== Recent log ==="
tail -10 /tmp/$JOB_NAME/$JOB_NAME/job.log 2>/dev/null \
  || tail -10 /tmp/$JOB_NAME.log 2>/dev/null \
  || tail -10 "$HOME/git/terminal-bench/jobs/$JOB_NAME/job.log" 2>/dev/null \
  || echo "(no log)"
REMOTE
