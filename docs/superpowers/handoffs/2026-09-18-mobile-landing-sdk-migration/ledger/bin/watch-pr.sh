#!/usr/bin/env bash
# watch-pr.sh PR HEAD9 — poll a PR head once a minute and print one line per
# state change worth acting on: a failed CI check, CI complete (all non-roborev
# checks terminal), the RoboRev combined verdict for exactly this head, and the
# head moving (which ends the watch). Exits when CI is complete AND RoboRev has
# a verdict, or after 120 polls in total (the CI-only tail shares the budget).
# Empty/missing check states count as pending. CI counts as complete only once
# at least MIN_CHECKS (default 10; override with WATCH_PR_MIN_CHECKS) non-roborev
# checks are present and terminal: the rollup fills in over the first minutes
# after a push and a lower floor fires a false CI COMPLETE on the partial set.
set -u
pr="$1"; head="$2"; head7="${2:0:7}"; ci_done=0; seen=""; mismatches=0; polls=0; min_checks="${WATCH_PR_MIN_CHECKS:-10}"
while [ $polls -lt 120 ]; do
  polls=$((polls+1))
  s=$(gh pr view "$pr" --json headRefOid,statusCheckRollup 2>/dev/null) || { sleep 60; continue; }
  cur=$(jq -r '.headRefOid[:9]' <<<"$s")
  if [ "$cur" != "$head" ]; then
    # The API can serve the previous head for a minute after a push; confirm
    # the move on three consecutive reads before retiring.
    mismatches=$((mismatches+1))
    if [ "$mismatches" -ge 3 ]; then echo "HEAD MOVED to $cur; stopping watcher"; exit 0; fi
    sleep 60; continue
  fi
  mismatches=0
  fails=$(jq -r '.statusCheckRollup[] | ((.conclusion // .state // "") | ascii_upcase) as $st | select($st | test("FAILURE|ERROR|CANCELLED|TIMED_OUT|ACTION_REQUIRED")) | "CI \(.name // .context): \($st)"' <<<"$s" | sort -u)
  comm -13 <(printf '%s\n' "$seen" | sort -u) <(printf '%s\n' "$fails") | grep -v '^$'
  seen=$(printf '%s\n%s' "$seen" "$fails")
  if [ $ci_done -eq 0 ] && jq -e '([.statusCheckRollup[] | select((.name // .context) != "roborev")] | length) >= '"$min_checks"' and ([.statusCheckRollup[] | select((.name // .context) != "roborev") | ((.conclusion // .state // "") | ascii_upcase)] | all(test("^(SUCCESS|FAILURE|ERROR|CANCELLED|TIMED_OUT|SKIPPED|NEUTRAL|ACTION_REQUIRED)$")))' <<<"$s" >/dev/null; then
    ci_done=1; echo "CI COMPLETE: $(jq -r '[.statusCheckRollup[] | select((.name // .context) != "roborev") | ((.conclusion // .state // "") | ascii_upcase)] | group_by(.) | map("\(.[0])=\(length)") | join(" ")' <<<"$s")"
  fi
  body=$(gh pr view "$pr" --json comments --jq '[.comments[] | select(.body | startswith("<!-- roborev-pr-comment -->"))] | last | .body' 2>/dev/null)
  if grep -q "Combined Review (\`$head7\`)" <<<"$body" && grep -qE "Reviewers: .*[0-9]+ done" <<<"$body"; then
    if grep -qiE "No issues found" <<<"$body" && ! grep -qE "^#{1,6}[[:space:]]*(Medium|High|Critical|Low)\b|^\*\*(Medium|High|Critical|Low)" <<<"$body"; then echo "ROBOREV CLEAN at $head"; else echo "ROBOREV NOT CLEAN at $head; $(grep -m1 -E '^## ' <<<"$body" | tail -1)"; fi
    [ $ci_done -eq 1 ] && exit 0
    # verdict in, CI still running: keep polling CI only
    while [ $ci_done -eq 0 ] && [ $polls -lt 120 ]; do
      polls=$((polls+1)); sleep 60
      s=$(gh pr view "$pr" --json headRefOid,statusCheckRollup 2>/dev/null) || continue
      [ "$(jq -r '.headRefOid[:9]' <<<"$s")" = "$head" ] || { echo "HEAD MOVED; stopping watcher"; exit 0; }
      jq -r '.statusCheckRollup[] | ((.conclusion // .state // "") | ascii_upcase) as $st | select($st | test("FAILURE|ERROR|CANCELLED|TIMED_OUT")) | "CI \(.name // .context): \($st)"' <<<"$s" | sort -u
      if jq -e '([.statusCheckRollup[] | select((.name // .context) != "roborev")] | length) >= '"$min_checks"' and ([.statusCheckRollup[] | select((.name // .context) != "roborev") | ((.conclusion // .state // "") | ascii_upcase)] | all(test("^(SUCCESS|FAILURE|ERROR|CANCELLED|TIMED_OUT|SKIPPED|NEUTRAL|ACTION_REQUIRED)$")))' <<<"$s" >/dev/null; then
        echo "CI COMPLETE: $(jq -r '[.statusCheckRollup[] | select((.name // .context) != "roborev") | ((.conclusion // .state // "") | ascii_upcase)] | group_by(.) | map("\(.[0])=\(length)") | join(" ")' <<<"$s")"; exit 0
      fi
    done
  fi
  [ $polls -lt 120 ] && sleep 60
done
echo "WATCHER TIMEOUT after 120 polls (ci_done=$ci_done)"
