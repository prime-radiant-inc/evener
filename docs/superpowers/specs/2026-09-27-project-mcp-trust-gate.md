# Project-layer MCP trust gate — design

Date: 2026-09-27
Status: design only; nothing implemented. Jesse picked "trust record + prompt"
for gating project-layer MCP servers (`.evener/mcp.json`) and asked whether the
right shape is "TOFU or maybe just manual selection at session start, like we
currently do with plugins?" This document answers that from the code: what the
plugin mechanism actually is, what the closest existing trust gate is, and what
the MCP gate should mirror. Every claim about current behavior carries a
file:line citation verified against the working tree on 2026-09-27.

## Problem

Any repository can ship `.evener/mcp.json`. Evener loads it at every session
start — new sessions and resumes alike — and connects everything it declares,
with no approval step:

- The project layer is Layer 2 of `mcpconfig.Discover`
  (agent/mcpconfig/config.go:342-359), loaded through `LoadFileUntrusted`
  (agent/mcpconfig/config.go:61-63, called at :351).
- "Untrusted" means exactly one refusal: a `$(command)` expression is rejected
  at parse time instead of executed
  (agent/mcpconfig/config.go:248-257, refusal at :253-255). Everything else —
  a bare stdio entry's `command`, `args`, `env` — parses and connects.
- Session init then connects every discovered server:
  `initMCP` (agent/session_init.go:2540-2579) runs `mcpconfig.Discover`
  (:2541), merges plugin-provided configs as a base layer (:2550-2553), and
  hands everything to `mcp.NewManager` (:2564), which spawns stdio server
  subprocesses. There is no gate between parse and spawn. Failures become
  warnings (:2545-2549, :2569-2575), flushed onto the session's event stream
  at session start (agent/session_events.go:182-186). `initMCP` runs inside
  `initSessionState` (:1761), which is shared by new sessions and the restore
  path (:533).
- The layer is model-writable — the loader's own comments say so
  (agent/mcpconfig/config.go:244-245, :343-345), and it is the reason
  `DiscoverTrusted` excludes the project layer when sandbox roots are derived
  (agent/mcpconfig/config.go:382-397; agent/sandbox_infra.go:42, :80).
  Nothing the model does mid-session can widen its sandbox via mcp.json —
  but the same file's *commands* run on the host at the next session start.

The attack this must stop: a bare stdio entry in a repo's `.evener/mcp.json`
executes its `command` field on the host when any session opens the repo,
silently. Two aggravating facts from the connect path:

- In a sandboxed, `net=off` session, stdio MCP servers are deliberately
  *not* severed from the network: `confineCommandUnderSandbox` uses
  `ConfineTrustedInfra`, which "keeps network access under net=off: MCP
  servers are trusted infrastructure" (agent/internal/mcp/manager.go:297-321,
  especially :314-319), and the server's environment gets the session's
  credential env floor applied last (:302-307). The premise of that trust —
  the comment at agent/internal/mcp/manager.go:280-283 claims MCP servers are
  "launched only from config layers the model cannot write" — is false for
  the project layer. The comment is evidence the current behavior is a stale
  assumption, not a decision; it should be corrected when this gate ships.
- The full-hub hub MCP is bearer-only by Jesse's decision of 2026-09-27
  (scoped tokens declined; docs/superpowers/specs/2026-09-27-hub-mcp-design.md:266-273),
  and the hub-mcp design already flags the project layer as the zero-config
  wiring hazard: a bare stdio entry pointing at the server's dist "wires
  full-hub power into a session zero-config" (same doc, :239-246). With no
  scoped tokens coming, a load-time trust gate is the structural mitigation.

The plugin-manifest route has the same shape: a plugin's `.mcp.json` and its
inline `mcpServers` entries parse through the same untrusted path
(agent/plugin/plugin.go:154-197, `ParseServerMapUntrusted` at :177 and :220),
and `initPlugins` appends the results for `initMCP` to connect
(agent/session_init.go:2037). Plugin content can change after the operator
chose it (auto-upgrade; model edits to plugin directories living inside a
repo), so install-time consent alone does not cover what the entries say
*today*.

## What exists today

### The plugin mechanism Jesse remembered: manual selection at session start

It exists, exactly as he described it — but it is a *selection*, not a trust
gate:

- A per-session plugin allow-list: `--enabled-plugins` on the CLI
  (docs/superpowers/specs/2026-08-26-plugin-session-toggle-design.md:27-28,
  :117-125), the `evener/plugin/preview` RPC, the web launcher's "Plugins for
  this session" disclosure, and the TUI's dedicated new-session picker
  (cmd/evener-tui/internal/launchconfig/plugins_for_launch_panel.go:16-45).
- It keys on plugin manifest name, applies to one session only, and is never
  persisted (spec, :126-135 and the snapshot-boundary section).

What it selects *among* is already operator-chosen. Plugin trust today rests
on two acts outside session start:

- Install-time confirmation — `evener plugin install` prompts "Plugins are
  arbitrary code. Pass --yes to confirm." (cmd/evener/plugincmd.go:421,
  :433-434); marketplace add prompts the same
  (cmd/evener/plugincmd.go:111, :123-124).
- The global installed-plugin registry — `~/.config/evener/plugins/installed_plugins.json`
  (internal/plugins/paths.go:46-50, :55) with a per-entry `Enabled` flag
  (internal/plugins/registry.go:29) the plugin managers toggle
  (internal/plugins/install.go:322).

There is **no prompt for repo-carried plugins**, because plugins never load
from repo content unprompted: `PluginDirs` reach a session only via explicit
`--plugin-dir`, the installed registry, or launch-config layers — and the
repo launch layer's `plugin_dirs` can only point inside a repo whose
`.evener/launch.toml` is already trust-approved, and only at repo-relative
paths (cmd/evener-hub/internal/launchconfig/resolver.go:229-231). Plugins
never needed a session-start prompt; MCP project-layer servers do, because
unlike plugins they load with no operator act at all.

### The mechanism to mirror: the `.evener/launch.toml` TOFU record

Evener already ships a trust record + prompt for exactly this class of file —
an in-repo, model-writable config that must not apply silently:

- Five states: `absent`, `untrusted`, `trusted`, `changed`, `rejected`
  (cmd/evener-hub/internal/launchconfig/types.go:94-103).
- Trust keys on a canonical content hash — sha256 of the re-encoded TOML, so
  whitespace and key order don't break it but any semantic edit does
  (cmd/evener-hub/internal/launchconfig/trust.go:14-35).
- The record is per-project: `<state root>/projects/<project id>/meta.toml`
  holds `MetaTrust{Hashes, Decision, DecidedAt}`
  (cmd/evener-hub/internal/launchconfig/types.go:120-136;
  cmd/evener-hub/internal/launchconfig/paths.go:19, :46), keyed on the
  path-derived project identity that
  canonicalizes to the git main checkout, so worktree switches keep the
  identity (identifier/project.go:54-102, :164-177;
  cmd/evener-hub/internal/launchconfig/paths.go:22-29).
- `ComputeTrustState`: first contact → `untrusted`; hash in the recorded set
  + decision trusted → `trusted`; same but rejected → `rejected` (stays
  blocked); hash not in the set → `changed`
  (cmd/evener-hub/internal/launchconfig/trust.go:62-90).
- The hash set is append-only, so branch-switching between previously
  approved contents does not re-prompt, and a rejected hash never becomes
  trusted by a later decision for a different hash
  (cmd/evener-hub/app_launch.go:221-237).
- Enforcement is at the load: the resolver applies the repo layer only when
  the state is `trusted`; otherwise the layer is skipped and its content is
  carried back as a `Preview` plus a diagnostic
  (cmd/evener-hub/internal/launchconfig/resolver.go:82-86, :147-181).
- The trust act is explicit and hash-CAS'd: the hub RPC
  `evener/launch/trustRepo` (appwire/types.go:108) takes `cwd` + `hash`
  (appwire/types.go:3659-3662) and refuses with "file changed since review"
  if the file re-hashes differently between the operator's review and the
  click (cmd/evener-hub/app_launch.go:210-212). The TUI shows the state, the
  preview, and "[T] trust this file"
  (cmd/evener-tui/internal/launchconfig/launch_settings_panel.go:296-309,
  Enter-to-trust at :320-330).

This is TOFU in shape — first use recorded, re-prompt only on change — with
the operator's keypress as the first-use trust act. That is what the MCP gate
should mirror. Departures forced by MCP's load topology:

- **Where the gate runs.** Launch.toml is read only by the hub, so its gate
  lives in the hub's resolver. `.evener/mcp.json` is read by the agent, in
  every process, hub-spawned or a direct `evener` run, new session or resume
  (agent/session_init.go:533, :1761) — so the MCP gate must live in the
  config load itself (mcpconfig), not in the hub.
- **Where the record lives.** The hub's `meta.toml` is hub state; a direct
  CLI session has no hub. The MCP record must live in the user's config
  layer, which every evener process already resolves
  (agent/mcpconfig/config.go:399-407; the plugin registry precedent,
  internal/plugins/paths.go:46-50).
- **Granularity.** Launch trust hashes one file. mcp.json is a map of
  independent servers; the record should hash per entry (see D3).

## Decisions

### D1. Gate at the config load, not at connect, not in the hub

The gate belongs in mcpconfig's project-layer load (and the plugin MCP path
that feeds the same merge), so every consumer is covered uniformly: direct
CLI runs, `evener serve` daemons, resumes, subagents and delegates (which run
the same `initMCP` against the same repo), and any future caller of
`Discover`. A connect-time gate in `initMCP` alone would leave other
`Discover` callers ungated; a hub-side gate would miss direct CLI runs
entirely.

`DiscoverTrusted` (agent/mcpconfig/config.go:394-397) — used for sandbox-root
derivation (agent/sandbox_infra.go:80) — is untouched: it already excludes the
project layer.

### D2. The trust record: the user's config layer, per project, per server entry

Location: `~/.config/evener/mcp-trust.json` (resolved via the same
`userdirs.ConfigRoot` as the global mcp.json, agent/mcpconfig/config.go:405-407).
One JSON file, schema-versioned like the plugin registry
(internal/plugins/registry.go:17-20, version check at :52-53):

```json
{
  "schema": 1,
  "projects": {
    "evener-a1b2c3d4e5": {
      "servers": {
        "weather": {
          "hashes": ["sha256:…"],
          "decision": "trusted",
          "decided_at": "2026-09-27T19:02:11Z"
        }
      }
    }
  },
  "plugins": {
    "superpowers": {
      "servers": {
        "plugin_superpowers_svc": { "hashes": ["sha256:…"], "decision": "trusted", "decided_at": "…" }
      }
    }
  }
}
```

Rules:

- The agent **reads** this file; only operator surfaces **write** it — the
  trust CLI verb, the hub RPC, the TUI. No code path reachable from a
  session's model ever writes the record.
- Writes are atomic and append hashes, never overwrite the set — the launch
  trust semantics (cmd/evener-hub/app_launch.go:221-237).
- `decision: "rejected"` mirrors `ComputeTrustState`'s rule that a recorded
  rejected hash stays blocked (cmd/evener-hub/internal/launchconfig/trust.go:82-87).

Rejected locations: the repo itself (model-writable — the record would guard
itself with an attacker-editable file); hub state `projects/<id>/meta.toml`
(right shape, wrong owner — a direct `evener` run has no hub state root); a
new per-project file inside the user config dir (workable, but one more
top-level convention than a single versioned registry file, for no gain).

### D3. What it keys on: project identity + per-entry content hash

Weighed:

- **Repo path (raw).** Survives nothing: a move or clone is a new path, and
  evener's project identity already exists to fix exactly this — the
  path-derived ID canonicalizes to the git main checkout, so linked
  worktrees share identity (identifier/project.go:54-102;
  cmd/evener-hub/internal/launchconfig/paths.go:22-29). Launch trust already
  accepts the cost that a genuinely moved repo re-prompts once. Rejected as
  a *predicate*; adopted as the *record key*.
- **Git remote/origin URL.** Absent for local-only repos; forked and mirrored
  clones legitimately differ; and `.git/config` sits inside the workspace the
  model can write — the model can point `origin` wherever it likes before the
  session ends. A key the attacker edits is not a key. Rejected.
- **Content hash of each server entry.** The actual security property: the
  operator approved *these bytes*. Recommended, per entry (type, command,
  args, env, url, headers), hashed over a canonical encoding — parsed then
  re-marshaled with sorted keys — so whitespace and key order don't break it
  but any semantic edit does, the same design as `CanonicalHashTOML`
  (cmd/evener-hub/internal/launchconfig/trust.go:14-22).

Recommendation: **project identity + per-entry content hash**, the same pair
launch trust uses (project-keyed meta file + canonical content hash), so the
operator mental model is one mechanism.

Why per-entry rather than the whole file: mcp.json is a map of independent
servers. A whole-file hash means a benign second server added by a colleague
invalidates trust on the first and re-prompts for both; per-entry hashes mean
unrelated additions never disturb trusted entries — each addition is simply a
new first encounter, and an edit to an existing entry changes exactly that
entry's hash. This is launch trust's hash-set idea (append-only, per-version)
applied at entry granularity. Renames and clones: same content at a new path
is a new project identity → one first-encounter approval, which is correct —
a clone is a new trust surface. The model editing the file mid-session is
the case the hash exists for: the session that's already running doesn't
re-read the file, but the next start or resume re-runs `initMCP`
(agent/session_init.go:533, :1761), re-hashes, sees `changed`, and skips
until re-approved.

### D4. TOFU vs explicit prompt

Jesse leaned TOFU. Both options, honestly:

- **Silent TOFU** (record the hash on first encounter, connect, re-prompt
  only on change): zero friction, but it gates nothing that matters. The
  attack *is* the first encounter — a hostile repo's mcp.json executes its
  command the one time silent TOFU is busy recording the hash. Afterward the
  operator has a durable record of a compromise that already happened.
  Silent TOFU is a change-detector, not a gate. Rejected.
- **Explicit prompt on every session start:** asks every time, even for
  long-trusted entries, and trains the operator to click through. The
  hash-keyed record already catches every change, so this buys no security
  over the next option. Rejected.
- **TOFU with a first-use approval** — first encounter prompts (never
  connects first), approval records the entry hash, subsequent loads connect
  silently, any change re-prompts. This is precisely what
  `.evener/launch.toml` does today, and the only form of TOFU that stops the
  primary attack. **Recommended.**
- **Per-session manual selection** (the plugin picker model, Jesse's other
  memory): not the primary gate — it doesn't persist, doesn't cover headless
  sessions or resumes, and makes every launch a chore for the common
  one-server case. It remains a *narrowing* surface (see Non-goals).

So: TOFU, in the launch-trust shape. Jesse's "trust record + prompt" and his
TOFU lean are the same recommendation once the first-use act is explicit.

### D5. Session-start behavior

Evaluation happens inside the project-layer load at every `initMCP` run —
new session, resume, subagent, delegate alike:

- **Interactive session.** Untrusted and changed entries are *skipped, never
  connected*, and the session opens with a diagnostic warning per entry
  (Source `"mcp"`, the same `WarningData` path other MCP warnings ride,
  agent/session_init.go:2545-2549; agent/events/payloads.go:586-598) naming
  the server, the file, and the state. The operator then approves through:
  the trust CLI verb (`evener mcp trust <repo> [--server name]`), the TUI
  affordance (mirroring the launch repo tab's "[T] trust this file",
  cmd/evener-tui/internal/launchconfig/launch_settings_panel.go:305-307), or
  the hub RPC (see D7). Approval is hash-CAS'd like `trustRepo` — "file
  changed since review" (cmd/evener-hub/app_launch.go:210-212) — because the
  model can edit the file between the operator reading it and clicking. The
  prompt shows the entry's rendered form (expanded command/args/env) plus the
  hash. v1 applies trust at the *next* session start; hot-connecting a
  just-trusted server into the running session is deliberately out (the
  session's MCP set is fixed at init, agent/session_init.go:2564-2577).
  A launcher-side pre-start preview (new-session form shows "this repo
  declares N untrusted MCP servers") mirrors the plugin picker and can
  follow; it is UI, not the gate.
- **Non-interactive / headless session** (daemon-spawned, `--non-interactive`,
  delegates): same rule — skip, never connect — with a warning that names
  exactly which servers were skipped and how to trust them, e.g.:
  `MCP server "hub" in /repo/.evener/mcp.json skipped: not trusted (first encounter). Approve with: evener mcp trust /repo --server hub`.
  This rides the existing pending-warnings flush
  (agent/session_events.go:182-186), so it is visible on the stream and in
  the transcript without any new plumbing.
- **Previously trusted entry changes:** state `changed`; skip until
  re-approved, warning says the entry changed since it was trusted (and
  shows old→new hash). Same as launch trust's `changed`
  (cmd/evener-hub/internal/launchconfig/trust.go:79-80).
- **Parse failures** keep today's behavior: skip-with-warning naming the file
  (agent/mcpconfig/config.go:351-354).

### D6. Plugin-manifest MCP entries: same gate, keyed by plugin identity

Plugin `.mcp.json`/`mcpServers` entries flow through the same untrusted
parse (agent/plugin/plugin.go:177, :220) into the same connect
(agent/session_init.go:2037, :2550-2553), so they get the same gate, keyed in
the record's `plugins` section by manifest name — the loader's own identity
boundary (agent/plugin/plugin.go:335-341; the session picker made the same
choice, spec :126-135) — with the entry hash as the predicate.

The operator's existing consent covers first contact: `evener plugin install`
writes the initial trust entries for the plugin's MCP configs as part of the
install the confirmation just approved (cmd/evener/plugincmd.go:433-434). A
plugin that later gains or changes an MCP entry — via marketplace upgrade,
auto-upgrade, or a model editing a plugin directory that lives inside a repo
— presents a new hash: skip + warning + prompt at the next session start.
Install consent covers the bytes installed, not bytes shipped afterward.

Tradeoff stated plainly: upgrades that change MCP entries will prompt once
per change. That is the honest cost of "the operator approved these bytes";
auto-carrying trust across content changes would recreate the hole the gate
exists to close (open question Q3 for whether auto-upgrade should surface
this more loudly at upgrade time).

### D7. Mechanics (function-level sketch; no code)

- **mcpconfig — new trust.go**: load/save the record (atomic write,
  schema-versioned, mirroring internal/plugins/registry.go:40-73); canonical
  per-entry hash; a `ComputeTrustState`-equivalent with launch trust's exact
  state table (cmd/evener-hub/internal/launchconfig/trust.go:62-90), so
  `rejected` stays blocked.
- **mcpconfig — `Discover` Layer 2** (agent/mcpconfig/config.go:342-359):
  after `LoadFileUntrusted` parses, evaluate each entry against the record;
  keep trusted entries, drop the rest, and return per-server skip records
  (name, file, state, hash, rendered entry) alongside the existing
  `warnings`. The return shape grows a structured field (or a sibling
  `DiscoverGated`); global/CLI-file/inline layers (:329-340, :361-377) are
  untouched.
- **agent — `initMCP`** (agent/session_init.go:2541): convert the gate's
  skip records into `WarningData` (Source `"mcp"`, title distinguishing
  untrusted/changed, message naming the server, file, and trust command) —
  the same queue as today's config warnings (:2545-2549).
- **agent — plugin path**: `initPlugins` (agent/session_init.go:2037)
  hands `p.MCPConfigs` plus their origin (plugin manifest name) to the same
  evaluation under the `plugins` record section; skips warn with the plugin
  name, like today's plugin MCP warnings (:2038-2042).
- **Operator surfaces**: `evener mcp` verb group — `list` (show each
  project-layer and plugin entry, its state and hash) and `trust`
  (approve by path and optional server name, hash-CAS'd); hub RPC
  `evener/mcp/trust` mirroring `evener/launch/trustRepo`
  (appwire/types.go:108, params shape :3659-3662); TUI row in the session
  view mirroring the launch repo tab affordance
  (cmd/evener-tui/internal/launchconfig/launch_settings_panel.go:320-330).
  No `evener mcp` command exists today (only the `--mcp`/`--mcp-config`
  flags, cmd/evener/main.go:347-348), so the name is free.
- **Comment correction**: the "launched only from config layers the model
  cannot write" premise at agent/internal/mcp/manager.go:280-283 is false
  while the project layer connects ungated, and becomes true-ish only in the
  weaker sense "model-writable layers load only with recorded operator
  approval"; rewrite it with the gate.

### D8. Migration: intended break, stated plainly

Repos whose project-layer servers load today will stop loading until the
operator trusts them once. That is the point — today's "working" behavior is
the vulnerability — but it must be said loud: after this ships, opening a
repo that relied on `.evener/mcp.json` shows its servers skipped with a
warning naming each one and the one-line unblock:

```text
evener mcp trust /path/to/repo            # trust every current entry, hash-CAS'd
evener mcp trust /path/to/repo --server hub  # one entry only
```

Plugin-installed servers do not re-prompt on upgrade — the install flow seeds
their entries — except when an upgrade changes an entry (D6). Tests that pin
today's ungated behavior change with it: the plugin-config merge test that
expects a plugin server to connect with no trust state
(agent/cov_intg_mcp_test.go:108-135) and the mcpconfig project-layer tests;
see the test plan.

## Test plan

Per docs/developing-evener/agentic-testing.md conventions where they apply;
no network, no credentials, deterministic.

Unit (mcpconfig):

- Record round-trip: save/load, schema-version enforcement, atomic write,
  hash-set dedupe, append-only merge across two approvals.
- `ComputeTrustState` mirror, table-driven: absent / first-contact /
  trusted / changed / rejected-stays-blocked, matching
  cmd/evener-hub/internal/launchconfig/trust.go:62-90 semantics.
- Canonical entry hash: stable across whitespace and key reorder; unstable
  across any semantic change to command, args, env, url, headers, type.
- Loader gating: trusted entry loads; untrusted entry skipped and named;
  changed entry skipped and named; one trusted + one untrusted entry in the
  same file loads exactly the trusted one; parse failure still
  skip-with-warning (today's behavior, agent/mcpconfig/config.go:351-354);
  global/CLI-file/inline layers unaffected by any record state.

Integration (agent, real-subprocess harness as in
agent/cov_intg_mcp_test.go):

- A session opened in a repo with an untrusted project-layer server never
  connects it: no manager connection, no tool registered, and the flushed
  warning names the server, the file, and the trust command (assert via the
  pendingMCPWarnings flush, agent/session_events.go:182-186).
- The same repo after `evener mcp trust` connects it (tool registered).
- The same repo after the file is edited post-trust skips it again with a
  changed-state warning.
- Plugin variant: a plugin dir whose MCP entry hash is recorded connects; an
  unrecorded one skips with the plugin named.
- Headless variant (`--non-interactive`): same skips, same warning on the
  stream.
- Resume variant: restore re-evaluates current bytes (trusted connects,
  edited-since-trust skips).

E2E gate: `make test-hub-mcp-e2e`'s wiring fixture gains a project-layer
untrusted entry and asserts it stays unconnected while the trusted global
wiring still connects.

## Non-goals

- No change to trusted layers: the global `~/.config/evener/mcp.json`,
  `--mcp-config` files, and `--mcp` inline specs (agent/mcpconfig/config.go:329-340,
  :361-377) stay operator-authored and ungated.
- No change to `DiscoverTrusted` or sandbox-root derivation
  (agent/mcpconfig/config.go:382-397).
- No new `$(command)` permissions; untrusted layers keep refusing them
  (agent/mcpconfig/config.go:253-255).
- No hub token scoping (declined 2026-09-27;
  docs/superpowers/specs/2026-09-27-hub-mcp-design.md:266-273).
- No per-tool or per-call approval inside a connected server.
- No hot-connect of newly trusted servers into a running session.
- No plugin-picker-style per-session MCP allow-list; selection stays what it
  is (the plugin surface). If wanted later, it layers cleanly on top of the
  trust record.

## Security notes

- The record holds only hashes, names, and timestamps — no secrets — but is
  operator-owned state; write paths stay out of the session's reach.
- Threat model honesty: an unsandboxed session's model can already run
  anything, including editing `~/.config/evener/mcp-trust.json` with the
  shell tool. This gate's threat is *repo-borne config executing at session
  start* — including in sandboxed sessions, where the workspace is
  confined but MCP servers still get network (agent/internal/mcp/manager.go:314-319)
  and the credential env floor (:302-307). Sandboxed sessions cannot write
  the record (their filesystem roots exclude the config dir).
- Adjacent hole, out of scope here, recorded so it is not lost: a *trusted*
  `.evener/launch.toml` can point `mcp_configs` at a repo file
  (cmd/evener-hub/internal/launchconfig/resolver.go:231), and that file then
  loads through the trusted `--mcp-config` path with full `$(command)`
  expansion (agent/mcpconfig/config.go:361-368) — the launch trust gates the
  layer's bytes, not the referenced file's. Same class of bug, needs its own
  small decision (Q5).

## Open questions for Jesse

1. **Trust scope per approval.** One keypress trusts every entry in the
   current file (hash-CAS'd per entry), or the operator names entries
   (`--server`) every time? The design above defaults to all-current-entries
   with per-entry hashes recorded; the stricter default is one prompt per
   entry.
2. **Launcher pre-start preview.** Should the new-session forms (web/TUI)
   surface untrusted project-layer servers before start, plugin-picker
   style, or is warning + post-hoc trust command enough for v1?
3. **Auto-upgrade loudness.** When a marketplace upgrade changes a plugin's
   MCP entries, trust lapses silently until the next session warns. Should
   the upgrade output name the re-prompt up front?
4. **Rejected entries.** Should the CLI expose an explicit `evener mcp
   reject` (a "never prompt again for this hash" decision, mirroring launch
   trust's `rejected` state), or is ignore-in-record enough?
5. **Repo-referenced mcp_configs via trusted launch.toml** (Security notes):
   gate those loads by file origin too, or leave as trusted-layer behavior?
6. **Warning vs error in interactive sessions.** v1 warns and continues
   without the servers. Should an interactive session ever *refuse to start*
   when project-layer servers are untrusted (it cannot today for any MCP
   condition; failures are non-fatal by design, agent/session_init.go:2561-2563)?
