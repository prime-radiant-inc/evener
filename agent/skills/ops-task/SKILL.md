---
name: ops-task
description: "Fix, build, and configure tasks: install deps, try, read errors, fix, verify. Use for debugging broken builds, configuring services, and operational fixes."
---

# Ops Task

Fix it, build it, configure it, verify it works.

## The Loop

1. **Try it.** Run the command, build the project, start the service.
2. **Read the COMPLETE error.** Stack traces, log output, exit codes — all of it.
   Do not skim. The error message usually contains the answer.
3. **Fix one thing.** Make the smallest change that addresses the error.
4. **Try again.** Did the fix work? If yes, move to the next issue. If no, read
   the new error and fix that.

## Resolve Missing Dependencies

When a command fails with "not found" or an import fails with "No module named":
- Install the package before retrying: `pip install`, `apt-get install`, `npm install`.
- If `python` is not found, try `python3`.
- If a binary is missing, search for it (`which`, `find`, `dpkg -S`) or install it.
- A missing dependency is never a reason to give up — it is one command away.

## The 3-Strike Rule

After 3 failed attempts at the same fix strategy, change approach fundamentally:
- Different tool or library
- Different configuration method
- Different architecture
- Read the actual documentation instead of guessing

Do NOT attempt fix #4 with the same strategy. Your approach is wrong.

## Keep an Approach Log

Maintain `approaches.log` in the working directory. Record each attempt and why it
failed. When context compaction erases your earlier work, this file preserves it.
Before trying a new approach, read the log to avoid repeating what already failed.
Format: one line per attempt with what you tried and what went wrong.

## Clean Up Before Finishing

Remove temporary files, test scripts, and debug output you created. But NEVER delete:
- Files that are part of the deliverable (compiled libraries, build outputs, data files)
- Files the task specification mentions as expected output
- Running servers, daemons, or background services that are part of the deliverable

## Final State

When you finish, the system must be in the state the user would expect. If you were
asked to configure a server, the server is running. If you were asked to build something,
the built artifacts exist. If you were asked to deploy, the deployment is live. Verifying
that something *can* work is not the same as leaving it working.

## Verify Output

Do not trust "it seemed to work." Verify concretely:
- Files exist? Check with `ls`, `stat`, or `test -f`.
- Service running? Use `curl` to hit the endpoint, not just `ps`.
- Tests pass? Run the full test suite, read the output.
- Build succeeded? Check exit code AND look for warnings.
- Output correct? Compare against expected values, not just "non-empty."
