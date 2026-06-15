# compact-tool-pins-note-and-persists: agent calls `compact`, the note is pinned, survives the compaction, and lands in meta.json

**What this covers**: the agent-invoked self-compaction tool (branch
`self-compaction-tool`): the `compact` core tool
(`agent/session_tools_compact.go`), the pinned-note re-stamp inside
`runPreCompactHook` (`agent/session_compaction.go`), the round-tail force
(`agent/session_self_compact.go` `applyPendingForceCompact` →
`agent/session_lifecycle.go`), and persistence via `SessionMeta.PinnedNote`
(`agent/schema/snapshot.go`, `agent/session_state.go`). The unit/-race tests
(`agent/session_self_compact_test.go`, `…compact_test.go`) prove the wiring in
isolation; this proves the tool is advertised to a real model, the model can
invoke it, and the note reaches the authoritative on-disk record after a real
compaction runs.

## Pre-state

- Fresh binaries built from this branch, run in a **fully isolated** hub so the
  test cannot collide with a real hub or a sibling worktree's hub (`~/.serf`,
  the default port, and `~/.local/state/serf` are host-level singletons). Use a
  throwaway `$HOME` with the real provider creds symlinked in:
  ```bash
  cd <this worktree>
  go build -o /tmp/serf-sc-hub ./cmd/serf-hub
  go build -o /tmp/serf-sc     ./cmd/serf
  REALSERF=~/.serf
  TH=$(mktemp -d -t serf-sc-home-XXXXX)        # isolated HOME
  mkdir -p "$TH/.serf/run" "$TH/.local/state/serf/projects"
  # Symlink read-only creds + config that DON'T carry absolute state paths:
  for f in credentials.toml providers.toml auth-token launch.toml; do
    ln -s "$REALSERF/$f" "$TH/.serf/$f"; done
  # DO NOT `cp` the real hub.toml — it hardcodes absolute paths to /home/.../.serf
  # (hub_state_root, run_dir, state_glob, past_index_db) and the test hub will
  # then read/write REAL state. WRITE a fresh one pointed entirely at $TH:
  cat > "$TH/.serf/hub.toml" <<EOF
  addr = "127.0.0.1:9186"
  hub_state_root = "$TH/.serf"
  run_dir = "$TH/.serf/run"
  state_glob = "$TH/.local/state/serf/projects/*"
  past_index_db = "$TH/.serf/index.db"
  spawn_timeout = "30s"
  EOF
  HOME="$TH" XDG_STATE_HOME="$TH/.local/state" \
    /tmp/serf-sc-hub -addr 127.0.0.1:9186 -serf /tmp/serf-sc \
    >/tmp/serf-sc-hub.log 2>&1 &
  sleep 3
  # ISOLATION GATE — abort if the hub resolved real paths:
  grep -q "run_dir=$TH" /tmp/serf-sc-hub.log || { echo "NOT ISOLATED — abort"; exit 1; }
  TOKEN=$(cat "$REALSERF/auth-token")
  ```
- A **launchable** model whose **credentials the isolated `$HOME` can actually
  reach**. This is the catch (see Sharp edges): `launch.toml` lists the
  launchable models (often `openai/gpt-5.5`), but OpenAI/Anthropic keys may live
  in a keyring the throwaway `$HOME` can't read, while `credentials.toml` may
  only hold a different provider. Confirm a model that is BOTH in `launch.toml`
  AND whose key is in the symlinked `credentials.toml` (or export the matching
  `*_API_KEY` into the hub's env). If none qualifies, run this card on a host
  where the real `$HOME`'s default model has reachable creds.

## Steps

1. **Spawn a session whose prompt forces a `compact` call with a known token.**
   The note token (`SCNOTE-7F3A`) is what we assert on disk. Tell the agent to
   do a little work first (so history is non-trivial) and then call `compact`:
   ```bash
   PROMPT='Create a file note.txt containing the word hello. Then call the compact tool with note_to_self exactly "REMEMBER SCNOTE-7F3A: note.txt holds hello" and compaction_instructions "keep the file path, drop incidental chatter". After the tool returns, reply with literal DONE.'
   SID=$(curl -s -X POST -H "Content-Type: application/json" \
     -H "Authorization: Bearer $TOKEN" \
     -d "{\"prompt\":$(python3 -c 'import json,os;print(json.dumps(os.environ["PROMPT"]))'),\"model\":\"anthropic/claude-haiku-4-5-20251001\",\"working_dir\":\"$TH\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{}}" \
     http://127.0.0.1:9186/api/spawn | python3 -c 'import sys,json;print(json.load(sys.stdin).get("session_id",""))')
   echo "session: $SID"
   ```
   **Expected:** a non-empty `$SID`. Falsify: empty/`error` ⇒ spawn failed
   (check `/tmp/serf-sc-hub.log` — usually a model/creds problem, not the
   feature).

2. **Wait for the turn to reach idle** (the agent ran the tool and replied):
   ```bash
   for i in $(seq 1 60); do
     grep -q '"kind":"TURN_END"\|DONE' /tmp/serf-sc-hub.log && break; sleep 2
   done
   sleep 2
   ```
   (Polling the log is a convenience; the authoritative checks are steps 3-4.)

3. **Assert the note is in `meta.json` (the authoritative persisted record).**
   ```bash
   META=$(find "$TH/.local/state/serf" "$TH/.serf" -name "$SID.meta.json" 2>/dev/null | head -1)
   echo "meta: $META"; python3 -c "import json,sys;d=json.load(open('$META'));print('pinned_note=',repr(d.get('pinned_note')))"
   ```
   **Expected:** `pinned_note= 'REMEMBER SCNOTE-7F3A: note.txt holds hello'`.
   **Falsification:** key absent / empty / different text ⇒ the tool did not
   pin the note, or `Meta()`/persistence regressed (`session_state.go` →
   `SessionMeta.PinnedNote`). A `pinned_note` that is the *cleared* empty string
   when the agent passed a non-empty note is a clear-vs-pin bug.

4. **Assert the note was re-stamped into history as a `[NOTE TO SELF]` steering
   turn** (the compaction actually ran and preserved the note verbatim). Read
   the transcript JSONL:
   ```bash
   TR=$(find "$TH/.local/state/serf" "$TH/.serf" -name "$SID.jsonl" 2>/dev/null | head -1)
   grep -c "SCNOTE-7F3A" "$TR"; grep -o "\[NOTE TO SELF\]" "$TR" | head -1
   ```
   **Expected:** at least one line containing `SCNOTE-7F3A`, and a
   `[NOTE TO SELF]` marker present. **Falsification:** the token appears only in
   the original tool-call args but no `[NOTE TO SELF]` steering turn exists ⇒
   `runPreCompactHook` did not re-stamp the note (the headline survival
   guarantee broke). More than one *current* `[NOTE TO SELF]` block for a single
   compaction ⇒ the strip-then-restamp dedup (`stripPinnedNoteTurns`) regressed.

5. **(Optional) Confirm the tool's return is a prediction, not a false claim.**
   In the transcript, the `compact` tool result text should read "Note pinned. A
   compaction will run at the seam…" (future tense) — never "a summary ran".
   ```bash
   grep -o "Note pinned[^\"]*" "$TR" | head -1
   ```
   **Falsification:** a past-tense "compacted"/"summary ran" claim ⇒ the
   prediction wording regressed (the compaction is deferred to the round tail,
   so the tool cannot truthfully report an outcome).

## Cleanup

```bash
pkill -x serf-sc-hub          # exact PROCESS NAME — see Sharp edges
rm -rf "$TH" /tmp/serf-sc-hub /tmp/serf-sc /tmp/serf-sc-hub.log
```
Leave the real `~/.serf` hub and sibling worktrees untouched — the isolated
`$HOME` guarantees this test never wrote to them.

## Sharp edges

- **`pkill -f 'serf-sc-hub'` SHOOTS ITSELF.** Your own shell's argv contains the
  string `serf-sc-hub`, so `pkill -f` (match full args) kills the shell running
  the cleanup — the command dies mid-way with no output and the hub may survive.
  Use `pkill -x serf-sc-hub` (match the exact process *name*, which only the
  binary has, not the shell). Verify with `pgrep -x serf-sc-hub`.
- **Never `cp` the real `hub.toml`.** It hardcodes absolute paths
  (`hub_state_root`, `run_dir`, `state_glob`, `past_index_db`) into the real
  `~/.serf` / `~/.local/state/serf`, so a "copied-config" test hub reads and
  writes REAL state — defeating isolation. WRITE a fresh `$TH`-scoped one and
  gate on `run_dir=$TH` in the startup log before spawning.
- **Credential/launch split is the most likely blocker.** Spawn fails with
  `model is not configured for Serf launch` if the model isn't in `launch.toml`,
  or `provider credentials missing` if its key isn't reachable from the isolated
  `$HOME` (keys often live in an OS keyring, not `credentials.toml`). Pick a
  model that satisfies BOTH, or export its `*_API_KEY` into the hub's env.
- **Isolation is mandatory.** Without the throwaway `$HOME`/`XDG_STATE_HOME`,
  the test hub shares `~/.serf/hub.lock`, the projects dir, and the default port
  with a real hub or a sibling worktree (e.g. `kimi-effort`). Symlink creds
  read-only; keep all mutable state in `$TH`.
- **Model must actually emit the tool call.** A weak/declined model may describe
  compaction instead of calling the tool — then `pinned_note` stays empty. That
  is a *model* miss, not a feature bug; re-run or use a more capable model. The
  prompt names the exact arg values to minimise this.
- **meta.json is atomic** (`WriteFile`+`Rename`); a mid-write read sees the
  previous file — re-read if `pinned_note` is briefly absent right after the
  tool call.
- **Two possible state roots.** Depending on `XDG_STATE_HOME`, transcripts/meta
  live under `$TH/.local/state/serf/projects/**` or `$TH/.serf/projects/**`; the
  `find` across both covers it.
- **Short history still pins.** If the agent did little work, the summary layer
  may no-op (`len(history) <= PreserveRecentTurns`) — that is expected; the note
  pin + `[NOTE TO SELF]` re-stamp happen regardless (steps 3-4 still hold).
