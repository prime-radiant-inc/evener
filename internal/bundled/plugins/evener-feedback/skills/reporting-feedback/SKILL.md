---
name: reporting-feedback
description: Use when hitting a rough edge, confusing tool error, missing affordance, or workaround in Evener itself, or when asked to report Evener feedback.
---
# Reporting Evener feedback

Use the bundled Python 3 helper to search existing issues and merged PRs, check for updates, upvote open duplicates, or report new feedback (run via `python3`, since bundled copies strip execute bits):

```sh
python3 "<base_directory>/scripts/evener-feedback" check --query "<terms>" [--limit 10] [--json]
python3 "<base_directory>/scripts/evener-feedback" check-update [--json]
python3 "<base_directory>/scripts/evener-feedback" upvote <issue_number> [--dry-run] [--json]
python3 "<base_directory>/scripts/evener-feedback" comment <issue_number> --details "<detail>" [--tool <name>] [--dry-run] [--json]
python3 "<base_directory>/scripts/evener-feedback" report --title "<title>" --summary "<summary>" --details "<details>" [--category tool_error|schema_gap|prompt_gap|performance|bug|other] [--tool <name>] [--dry-run] [--json]
```

Add `--repo OWNER/REPO` to any subcommand to target a fork instead of `prime-radiant-inc/evener`.

## Workflow

1. **Search first.** Run `check --query "<terms>"` to inspect matching open issues, recently closed issues, and recently merged PRs alongside the running `evener` version.
2. **Check for a newer build when a closed issue or merged PR claims a fix.** Run `check-update` to compare the running build with the latest release and `main` commit. When an update is available, tell your human partner which issue or PR claims the fix and suggest `evener upgrade` (or `evener upgrade snapshot` for a fix on `main` ahead of the latest tagged release). Keep the current session running so your human partner chooses when to upgrade and restart.
3. **Upvote an open match when you have nothing new to add.** Run `upvote <issue_number>` to add a `+1` reaction rather than filing a duplicate or posting a "me too" comment.
4. **Comment on an open match when you have new reproduction detail.** Run `comment <issue_number> --details "<detail>" [--tool <name>]` to attach the running version and concrete reproduction evidence.
5. **Open a new issue when no match exists.** Run `report` with `--title`, `--summary`, and `--details` (plus optional `--category` and `--tool`). Pass `--dry-run` first to inspect a payload before writing to GitHub.

## Public-repository hygiene

`prime-radiant-inc/evener` is public. Post only minimal, sanitized reproduction details; share secrets, tokens, private customer or project code, and exploitable security vulnerabilities directly with your human partner instead of GitHub.
