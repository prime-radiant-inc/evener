#!/usr/bin/env bash
# rawreviews.sh PR [HEAD9] — print RoboRev's individual panel-member reviews for a PR
# straight from the daemon's SQLite store on magic-kingdom (where `roborev ci review`
# runs the review matrix), bypassing the synthesis step that has been returning
# "No review output generated" whenever members had findings (2026-09-18).
# Picks the newest panel for the PR (or the panel for HEAD9 when given) and prints,
# per member: agent, review type, status, and the full review output.
# Use --list to print only the member table (no outputs). Read-only.
set -u
pr="$1"; head="${2:-}"; list="${3:-}"
where="pr_number=$pr"; [ -n "$head" ] && where="$where and head_sha like '$head%'"
ssh -o BatchMode=yes magic-kingdom "cd ~/.roborev && sqlite3 -readonly reviews.db \"
with p as (select panel_run_uuid, head_sha, outcome, synthesis_agent from ci_pr_panels where $where order by id desc limit 1)
select '== panel ' || p.panel_run_uuid || ' head ' || substr(p.head_sha,1,9) || ' outcome ' || coalesce(p.outcome,'') || ' synthesis ' || coalesce(p.synthesis_agent,'') from p;
with p as (select panel_run_uuid from ci_pr_panels where $where order by id desc limit 1)
select '-- member ' || j.panel_member_index || ' ' || j.agent || '/' || coalesce(j.model,'') || ' type=' || j.review_type || ' status=' || j.status || ' job=' || j.id || ' verdict=' || coalesce(r.verdict_bool,'') || ' chars=' || coalesce(length(r.output),0) from review_jobs j left join reviews r on r.job_id=j.id, p where j.panel_run_uuid=p.panel_run_uuid and j.panel_role='member' order by j.panel_member_index;
$( [ "$list" = "--list" ] || echo "with p as (select panel_run_uuid from ci_pr_panels where $where order by id desc limit 1) select char(10) || '######## member ' || j.panel_member_index || ' (' || j.agent || ' ' || j.review_type || ')' || char(10) || coalesce(r.output, '<no output; error: ' || coalesce(j.error,'') || '>') from review_jobs j left join reviews r on r.job_id=j.id, p where j.panel_run_uuid=p.panel_run_uuid and j.panel_role='member' order by j.panel_member_index;" )
\""
