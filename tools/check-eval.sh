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
# Harbor nests results: /tmp/<job>/<job>/<task>__<hash>/
RESULTS_DIR="/tmp/$JOB_NAME/$JOB_NAME"
if [ ! -d "\$RESULTS_DIR" ]; then
    RESULTS_DIR="/tmp/$JOB_NAME"
fi
for d in \$RESULTS_DIR/*/; do
    [ -d "\$d" ] || continue
    task=\$(basename "\$d" | sed 's/__.*$//')
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
  || echo "(no log)"
REMOTE
