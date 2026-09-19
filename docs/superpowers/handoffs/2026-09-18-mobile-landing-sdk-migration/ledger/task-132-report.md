# Task 132 — PR #1145 RoboRev round 28

Status: done. Two commits, main already current, pushed, PR body appended.

- `4a74cdd6c` ci: scan the caller's arguments for -C, this time in the script
- `194a210fa` ci: read a caller's flag in one spelling, and forward -compiler
- Pushed head: `194a210fa` (merge already up to date at `eeff54b70`)

Why round 27 reported item 1 fixed when it was not: that round's edit changed
four things in one python pass; the pass aborted partway on an unrelated
assertion, so the file was never written, and I re-applied the other three from a
second script without re-applying this one. My check could not distinguish the
two states — `-run 'Test*'` matches no file in the module directory, so the glob
expanded to nothing and the joined-string loop behaved. The reviewers' case needs
a file literally named `-C` plus a pattern that expands to it.

Reproduction, with a file named `-C` in the repository root:
```
before  -short -count=1 -run '*' → exit 2, "-C is not supported here …"
after   -short -count=1 -run '*' → exit 1, no such message; go rejects the regexp
--C /tmp (after)                 → refused, as -C is
--race --tags=fixture            → go list argv: list -race -tags=fixture ./...
-compiler gc                     → go list argv: list -compiler gc ./...
```

Gates: `bash -n` all three; envvars PASS 0.40s, `.` PASS 59.02s, agent PASS
39.64s; `make lint-generated` PASS. Fixture rules from 122a in force (the decoy
`-C` file was created in the repo root and removed with python, no `rm`).

Concerns:
- The lesson from item 1 is about my tooling, not the script: a multi-part edit
  that aborts silently leaves a partial state, and a verification that cannot
  fail on the unfixed code proves nothing. I now check each edit's assertions
  land before moving on, and pick reproductions that fail on the old code.
- `go_flag` strips exactly one leading dash; `---tags` would still slip past both
  readers. Go itself rejects that spelling, so nothing downstream is misled.
