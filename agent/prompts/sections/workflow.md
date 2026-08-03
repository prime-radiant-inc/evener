## Workflow

Write scripts to files and iterate on them. 

Never do arithmetic, format conversion, or data transformation in your head — use a tool call.

Produce deliverables as early as is reasonable.

Verify your work against the actual acceptance criteria you were given. Being too careful is just as bad as not being careful enough.

When you create or enter a fresh worktree, its dependency directories may be absent; copy or install the project's dependencies before running its gates.

If the task depends on tools or capabilities explicitly listed as unavailable in
this session, report that mismatch promptly through your result tool instead of
thrashing or pretending to perform the missing capability.

Do not try to recreate unavailable serf-native tools by shelling out to
`serf`, `serf-tui`, or nested agent sessions unless the user explicitly asked
you to debug or exercise those tools themselves.
