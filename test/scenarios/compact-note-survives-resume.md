# compact-note-survives-resume: a pinned note survives daemon restart / resume and is re-stamped on the next compaction

**What this covers**: the resume path for the self-compaction note (branch
`self-compaction-tool`): `SessionMeta.PinnedNote` persisted in `Meta()` and
restored in `RestoreSessionFromMetaWithConfig` (`agent/session_init.go`). This
is the round-3 review fix — the note is restored from **structured meta**, not a
fragile history scan, so it cannot match a copy folded into a `TurnSummary`.
Unit coverage: `TestPinnedNote_SurvivesResume` in
`agent/session_self_compact_test.go`. This proves it end-to-end across a real
process restart.

## Pre-state

- The same fully-isolated hub setup as
  `compact-tool-pins-note-and-persists.md` Pre-state (one `mktemp` `$run`,
  throwaway `$HOME` at `$TH="$run/home"`, symlinked creds, `-addr 127.0.0.1:0`
  with `$PORT`/`$HUB`/`$HUBPID` read back from `$run/hub.log` and
  `$run/hub.pid`). Reach a state where a session has a
  pinned note on disk — run that scenario through step 3, OR re-pin here with a
  fresh token `SCNOTE-RES1`.

## Steps

1. **Establish a pinned note** (if not already): spawn a session prompting the
   agent to `compact` with `note_to_self "REMEMBER SCNOTE-RES1: keep this across restart"`.
   Wait for idle. Confirm `pinned_note` is in `meta.json` (as in the core
   scenario step 3). Capture `$SID`.

2. **Restart the daemon** (simulate a crash/restart). Kill the hub by the pid
   you captured — never `kill %1`, which depends on this being the same shell
   that backgrounded it — then start a fresh hub with the same isolated
   `$HOME`/state so it reloads the persisted session. The second hub binds its
   own kernel-assigned port, so `$PORT`/`$HUB` must be re-read; that is the
   whole reason nothing here may hardcode one:
   ```bash
   kill "$HUBPID" 2>/dev/null; sleep 1
   HOME="$TH" XDG_STATE_HOME="$TH/.local/state" \
     "$run/serf-hub" -addr 127.0.0.1:0 -serf "$run/serf" \
     >"$run/hub2.log" 2>&1 &
   HUBPID=$!
   echo "$HUBPID" >"$run/hub.pid"
   for i in $(seq 1 50); do
     PORT=$(grep -oE 'listening on 127\.0\.0\.1:[0-9]+' "$run/hub2.log" 2>/dev/null | grep -oE '[0-9]+$') || true
     [ -n "$PORT" ] && break
     kill -0 "$HUBPID" 2>/dev/null || { echo "hub exited before listening:" >&2; cat "$run/hub2.log" >&2; exit 1; }
     sleep 0.1
   done
   [ -n "$PORT" ] || { echo "hub never logged a listening port" >&2; exit 1; }
   HUB=http://127.0.0.1:$PORT
   ```

3. **Resume the session and drive one more compaction.** Send a follow-up turn
   that does a little work and calls `compact` again with an EMPTY
   `compaction_instructions` (so it re-compacts without changing the note),
   asking the agent NOT to change `note_to_self`. Send via the resume/turn API
   (use the same `/api/spawn`-style turn endpoint the workspace uses for an
   existing `$SID`; see how `web-steer-*`/`reconnect-auto-resume.md` send a turn
   to an existing session). A simple prompt:
   `"Append a line to note.txt, then call compact with note_to_self unchanged (re-send REMEMBER SCNOTE-RES1: keep this across restart) and empty compaction_instructions. Reply DONE."`
   Wait for idle.

4. **Assert the note survived the restart and is still exactly one verbatim
   `[NOTE TO SELF]`** in the post-restart transcript/meta:
   ```bash
   META=$(find "$TH/.local/state/serf" "$TH/.serf" -name "$SID.meta.json" | head -1)
   python3 -c "import json;d=json.load(open('$META'));print('pinned_note=',repr(d.get('pinned_note')))"
   TR=$(find "$TH/.local/state/serf" "$TH/.serf" -name "$SID.jsonl" | head -1)
   # count NOTE TO SELF blocks AFTER the most recent compaction boundary
   grep -c "SCNOTE-RES1" "$TR"
   ```
   **Expected:** `pinned_note` still equals `REMEMBER SCNOTE-RES1: keep this
   across restart` after the restart; the token survives into the post-restart
   compaction. **Falsification:** `pinned_note` is empty after restart ⇒ restore
   path (`session_init.go`) did not repopulate `s.pinnedNote`, so the next
   compaction stamped nothing and the durable carry was lost — the exact
   round-3 bug this guards. Two divergent `[NOTE TO SELF]` blocks (one stale from
   a folded summary, one fresh) ⇒ a history-scan reconstruction crept back in
   instead of meta restore.

## Cleanup

```bash
kill "$HUBPID" 2>/dev/null    # the SECOND hub's pid, re-captured in step 2
rm -rf "$run"                 # $TH, both logs and the binaries all live under it
```

Kill by the recorded pid, and remove `$run` by name — never `pkill -f
'serf-hub …'` (it takes out every other concurrent agent's test hub) and never
an `rm -rf /tmp/serf-sc-home-*` glob (it deletes every other concurrent run of
this same card, kata `k2rx`).

## Sharp edges

- **The restart must reuse the SAME `$HOME`/`XDG_STATE_HOME`** so the second hub
  reads the first's persisted `meta.json`; a different temp dir starts empty and
  the test is vacuous.
- **Resume reads `[last compaction turn] + after`.** The reloaded transcript may
  contain the *previous* `[NOTE TO SELF]` turn; correctness depends on
  `meta.PinnedNote` (structured) driving the next re-stamp, not on matching that
  reloaded turn. The assertion is on `meta.json` first, transcript second.
- **Sending a turn to an existing session** uses the hub's per-session turn
  endpoint, not `/api/spawn`; confirm the exact route from a working steer/resume
  scenario before scripting step 3.
