---
name: harness-feedback
description: Use when hitting a rough edge, confusing tool error, missing affordance, or workaround in Evener itself, or when asked to report harness feedback.
---
# Harness feedback

Report Evener rough edges through the bundled helper so existing issues stay deduplicated and already-fixed builds prompt an upgrade. Run it with Python 3 (bundled copies strip execute bits):

```sh
python3 "<base_directory>/scripts/harness-feedback" <subcommand>
```

## Workflow

1. **Search first.** Run `check --query "<terms>"` to inspect matching open and recently closed issues in `prime-radiant-inc/evener` alongside the running `evener` version.
2. **Check for a newer build when a closed issue claims a fix.** Run `check-update` to compare the running build with the latest release and `main` commit. When an update is available, tell your human partner which issue claims the fix and suggest `evener upgrade` (or `evener upgrade snapshot` for a fix on `main` ahead of the latest tagged release). Keep the current session running so your human partner chooses when to upgrade and restart.
3. **Upvote an existing open issue when you have nothing new to add.** Run `upvote <issue_number>` to add a `+1` reaction rather than filing a duplicate or posting a "me too" comment.
4. **Comment on an existing open issue when you have new reproduction detail.** Run `comment <issue_number> --details "<detail>" [--tool <name>]` to attach the running version and concrete reproduction evidence.
5. **Open a new issue only when no match exists.** Run `report --title "<title>" --summary "<summary>" --details "<details>" [--category tool_error|schema_gap|prompt_gap|performance|bug|other] [--tool <name>]`. Pass `--dry-run` first to inspect a payload before writing to GitHub.

## Public-repository hygiene

`prime-radiant-inc/evener` is public. Post only minimal, sanitized reproduction details; share secrets, tokens, private customer or project code, and exploitable security vulnerabilities directly with your human partner instead of GitHub.
