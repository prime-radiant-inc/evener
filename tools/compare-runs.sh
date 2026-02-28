#!/usr/bin/env bash
# Compare results of two eval runs on flower-garden.
#
# Usage:
#   tools/compare-runs.sh <job-name-A> <job-name-B>
#
# Example:
#   tools/compare-runs.sh reviewer-full2 reviewer-full3
set -euo pipefail

REMOTE=jesse@192.168.118.101

JOB_A="${1:?Usage: compare-runs.sh <job-name-A> <job-name-B>}"
JOB_B="${2:?Usage: compare-runs.sh <job-name-A> <job-name-B>}"

ssh "$REMOTE" bash <<REMOTE
# Find the results directory for a job name.
find_results_dir() {
    local job="\$1"
    for candidate in "/tmp/\$job/\$job" "/tmp/\$job" "\$HOME/git/terminal-bench/jobs/\$job"; do
        if [ -d "\$candidate" ]; then
            if ls "\$candidate"/*/verifier/reward.txt >/dev/null 2>&1 || \
               ls "\$candidate"/*/reward.txt >/dev/null 2>&1; then
                echo "\$candidate"
                return
            fi
        fi
    done
    echo ""
}

# Get reward for a task directory.
get_reward() {
    cat "\$1/verifier/reward.txt" "\$1/reward.txt" 2>/dev/null | head -1
}

DIR_A=\$(find_results_dir "$JOB_A")
DIR_B=\$(find_results_dir "$JOB_B")

if [ -z "\$DIR_A" ]; then
    echo "ERROR: No results found for $JOB_A" >&2
    exit 1
fi
if [ -z "\$DIR_B" ]; then
    echo "ERROR: No results found for $JOB_B" >&2
    exit 1
fi

# Collect all task names from both runs.
declare -A rewards_a rewards_b
all_tasks=""

for d in \$DIR_A/*/; do
    [ -d "\$d" ] || continue
    task=\$(basename "\$d" | sed 's/__.*\$//')
    reward=\$(get_reward "\$d")
    [ -z "\$reward" ] && continue
    rewards_a["\$task"]="\$reward"
    all_tasks="\$all_tasks \$task"
done

for d in \$DIR_B/*/; do
    [ -d "\$d" ] || continue
    task=\$(basename "\$d" | sed 's/__.*\$//')
    reward=\$(get_reward "\$d")
    [ -z "\$reward" ] && continue
    rewards_b["\$task"]="\$reward"
    all_tasks="\$all_tasks \$task"
done

# Deduplicate and sort tasks.
all_tasks=\$(echo \$all_tasks | tr ' ' '\n' | sort -u)

pass_a=0; pass_b=0; total=0
improved=0; regressed=0

printf "%-35s  %-8s  %-8s  %s\n" "Task" "$JOB_A" "$JOB_B" ""
printf "%-35s  %-8s  %-8s  %s\n" "---" "---" "---" ""

for task in \$all_tasks; do
    ra=\${rewards_a[\$task]:-"-"}
    rb=\${rewards_b[\$task]:-"-"}

    # Convert to PASS/FAIL for display.
    if [ "\$ra" = "1.0" ] || [ "\$ra" = "1" ]; then
        da="PASS"; pass_a=\$((pass_a+1))
    elif [ "\$ra" = "-" ]; then
        da="-"
    else
        da="FAIL"
    fi

    if [ "\$rb" = "1.0" ] || [ "\$rb" = "1" ]; then
        db="PASS"; pass_b=\$((pass_b+1))
    elif [ "\$rb" = "-" ]; then
        db="-"
    else
        db="FAIL"
    fi

    total=\$((total+1))
    marker=""
    if [ "\$da" = "FAIL" ] && [ "\$db" = "PASS" ]; then
        marker="<- improvement"
        improved=\$((improved+1))
    elif [ "\$da" = "PASS" ] && [ "\$db" = "FAIL" ]; then
        marker="<- REGRESSION"
        regressed=\$((regressed+1))
    fi

    printf "%-35s  %-8s  %-8s  %s\n" "\$task" "\$da" "\$db" "\$marker"
done

echo ""
echo "Summary: $JOB_A=\$pass_a/\$total  $JOB_B=\$pass_b/\$total  +\$improved improved  -\$regressed regressed"
REMOTE
