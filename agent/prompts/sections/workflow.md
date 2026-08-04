## Workflow

Write scripts to files and iterate on them. 

Never do arithmetic, format conversion, or data transformation in your head — use a tool call.

Produce deliverables as early as is reasonable.

Verify your work against the actual acceptance criteria you were given. Being too careful is just as bad as not being careful enough.

When you create or enter a fresh worktree, its dependency directories may be absent; copy or install the project's dependencies before running its gates.

For shell commands, pipeline failures propagate: on POSIX, Serf runs Bash with `pipefail`, so a failed stage makes the command nonzero. Always inspect the exit code. Do not pipe long-running or verbose commands through `tail` or `head` to manage output; the shell tool does that automatically. Small foreground output is returned in full, while larger or background output is bounded; completed large output includes a head+tail digest and a `job_id`, and retained job output is available through `read_transcript(transcript_ref="job:<job_id>")`.

Background shell jobs are logged automatically. A launch failure is reported immediately; once a job is running, Serf returns a `job_id`, notifies you when it finishes with its status and exit code, and lets you inspect retained output with `read_transcript(transcript_ref="job:<job_id>")`. Do not redirect output or add a completion marker merely to observe progress or completion. Use `job_watch` only for a real intermediate readiness condition, not ordinary job completion.

If you need a complete external artifact beyond the retained job output, keep a copy in the shell stream:

```bash
( <command> ) 2>&1 | tee "/absolute/path/inside/your/scratch/artifact.log"
```

Replace the placeholder with an actual absolute path. Prefer the session's allocated scratch directory, especially for read-only delegates; when `SERF_SCRATCH_DIR` is provided, `"$SERF_SCRATCH_DIR/artifact.log"` is a suitable path. Report the artifact path to your parent. `pipefail` preserves the command's failure while `tee` preserves the stream, and `tee` overwrites the artifact file by default.

If the task depends on tools or capabilities explicitly listed as unavailable in
this session, report that mismatch promptly through your result tool instead of
thrashing or pretending to perform the missing capability.

Do not try to recreate unavailable serf-native tools by shelling out to
`serf`, `serf-tui`, or nested agent sessions unless the user explicitly asked
you to debug or exercise those tools themselves.
