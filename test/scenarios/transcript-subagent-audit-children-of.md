# transcript-delegate-audit-children-of: audit a delegate child transcript

**What this covers**: transcript discovery for delegate child sessions. A parent
delegates work, then uses transcript tools to enumerate and inspect the child
conversation instead of treating job output as the full transcript.

## Steps

1. Start a real Serf run with a fresh scenario state dir.
2. Ask the parent:

   > Use `delegate` with `background=false` for this task: "Create a file
   > inventory.txt in the current directory listing exactly these three words,
   > one per line: apples, oranges, pears. Then run wc -l inventory.txt to
   > confirm it has 3 lines, and report the line count."
   > After the delegate returns, relay its reported line count and confirm the
   > file exists. Then use `find_session_transcripts` with
   > `children_of:"<parent transcript ref>"` to enumerate the child, and use
   > `read_session_transcript` in outline/markdown mode to verify the child
   > actually ran the commands it claims.

## Expected

- The delegate result includes a child `transcript_ref`.
- `find_session_transcripts({children_of: ...})` returns the delegate child
  transcript with `parent_ref` set.
- The outline maps the child turns, and a markdown read of the relevant range
  shows the file creation and `wc -l` check.
- The parent distinguishes `job_read_output` as result/report output from
  transcript tools as the audit surface.
