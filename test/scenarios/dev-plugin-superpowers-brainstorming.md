# dev-plugin-superpowers-brainstorming: serf picks up brainstorming skill from a cloned plugin dir

**What this covers**: end-to-end smoke test of the `--plugin-dir`
mechanism (exposed via `launch_overrides.pluginDirs` on
`/api/spawn`). Clones the upstream Superpowers plugin into a run-
specific tmpdir, points a serf session at it, and confirms the
agent can see the `brainstorming` skill in its catalog.

This is a load test (does the plugin wire in?), not a behavior
test (does brainstorming actually work?). The behavior surface is
heavy and worth its own scenario.

## Pre-state

- Hub running with `--serf` resolvable.
- OpenAI OAuth signed in (or an alternative model the harness
  knows about).
- `git` on PATH. Network access to GitHub.

## Steps

1. Create a hermetic tmpdir and clone Superpowers into it. Shallow
   clone keeps the test small (~10 MB):
   ```bash
   tmpdir=$(mktemp -d -t serf-e2e-superpowers-XXXXX)
   git clone --depth=1 https://github.com/obra/superpowers "$tmpdir/superpowers"
   ```
2. Sanity-check the layout: the plugin root has a `skills/`
   directory with `brainstorming/SKILL.md` inside it. If not, the
   upstream repo restructured — update this scenario.
   ```bash
   ls "$tmpdir/superpowers/skills/brainstorming/SKILL.md"
   ```
3. Spawn a serf session with the plugin dir in `launch_overrides`.
   The wire key is `pluginDirs` (camelCase, per `internal/appwire/types.go`):
   ```bash
   TOKEN=$(cat ~/.serf/auth-token)
   curl -s -X POST -H "Content-Type: application/json" \
        -H "Authorization: Bearer $TOKEN" \
        -d "{\"prompt\":\"Inspect your available skills. Specifically: do you have access to a 'brainstorming' skill (likely listed as 'superpowers:brainstorming' or similar)? If yes, list its first paragraph of description. If no, say so plainly. Do not invoke the skill — just report whether you can see it.\",\"model\":\"openai/gpt-5.5\",\"working_dir\":\"$tmpdir\",\"harness\":\"serf\",\"branch\":\"\",\"access_mode\":\"full\",\"agent\":\"default\",\"launch_overrides\":{\"pluginDirs\":[\"$tmpdir/superpowers\"]}}" \
        http://localhost:9180/api/spawn
   ```
   Capture `session_id`.
4. Wait for a `communicate` tool call (max ~90s):
   ```bash
   deadline=$((SECONDS + 90))
   while [ $SECONDS -lt $deadline ]; do
     if find ~/.local/state/serf/projects -name "<sid>.transcript.jsonl" \
          -exec grep -l '"communicate"' {} \; 2>/dev/null | grep -q .; then
       break
     fi
     sleep 2
   done
   ```
5. Inspect the transcript:
   ```bash
   find ~/.local/state/serf/projects -name "<sid>.transcript.jsonl" \
     -exec cat {} \;
   ```

## Expected

- Transcript contains a `STEERING` entry early on that includes the
  `using-superpowers` skill content (`<EXTREMELY_IMPORTANT>You have
  superpowers...`). This is serf injecting the plugin's introduction
  skill at session start — proof the plugin dir was discovered.
- The agent's `communicate` message confirms it can see the
  `brainstorming` skill and quotes (loosely) from its description
  ("creating features, building components, adding functionality,
  or modifying behavior" — verbatim from the upstream SKILL.md's
  frontmatter).
- Falsification: the agent reports "no brainstorming skill" or
  describes a different / hallucinated skill, OR the STEERING
  entry is absent — the plugin dir didn't load.

## Cleanup

```bash
rm -rf "$tmpdir"
```

The session metadata under `~/.local/state/serf/projects/...`
lingers but is harmless. Worth `find ... -name "<sid>*" -delete`
between runs if you care about a clean past index.

## Sharp edges

- The plugin discovery follows Claude's plugin convention: the
  `pluginDirs` entries are plugin ROOT directories (containing
  `skills/`, optionally `commands/`, `hooks/`, etc.). Pointing at
  `skills/` directly will fail discovery.
- Upstream restructure risk: if `obra/superpowers` reshuffles its
  layout (e.g. moves skills into a versioned subdir), step 2's
  sanity check fires first. Update the path expectations.
- The introductory `using-superpowers` skill auto-injects as a
  STEERING turn — that's by Superpowers design, not a serf
  behavior. The presence of that injection is a strong loaded-
  successfully signal even before the agent's communicate reply.
- This test does NOT verify skill EXECUTION (running brainstorming
  in a real conversation). That's a richer scenario with its own
  fidelity requirements (interactive question flow, design
  approval gate, etc.). The kata "dev-skill-brainstorming-flow"
  is the next-up extension.
- Network dependency: the clone needs GitHub access. In an air-
  gapped CI, pre-cache the repo or vendor a tarball.
- `--depth=1` clones omit history — fine for our purposes. Don't
  rely on `git log` inside the cloned dir if your scenario needs
  it.
- The `launch_overrides.pluginDirs` field is a per-session
  override that takes precedence over global / in-repo launch
  config. To make a plugin available across all sessions, set it
  in `~/.serf/launch.toml` or via `serf-hub`'s settings UI under
  Launch Defaults → Plugin Dirs.
