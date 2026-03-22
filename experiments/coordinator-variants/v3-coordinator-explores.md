Remove explorer subagent entirely for the survey phase. The coordinator does its own
exploration using its read-only tools (glob, grep, read_file, shell).

Change to "How to work" step 1:
   **Survey the workspace yourself.** Use your own tools — glob, grep, read_file, shell —
   to understand what's in the workspace. List files, read tests, run any existing
   executables. Do NOT delegate exploration to a subagent — the round-trip wastes time
   and you lose the context. You need this information in YOUR context to plan well.

This eliminates the explorer overhead entirely. The coordinator sees everything firsthand.
