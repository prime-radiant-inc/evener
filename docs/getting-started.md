# Getting Started with Evener

Evener is a coding agent. Its default interactive surface is the browser:
the `evener-hub` orchestrator serves a web UI where you start sessions,
watch the agent work, and steer it with follow-up messages. The `evener-tui`
terminal dashboard and a non-interactive command line serve other workflows,
but the hub is the everyday way to use Evener.

This guide walks a new user from install to a working session. It points to
reference docs for details that change; follow the links where you need
depth.

## Install

Install the latest release on Linux x64 or macOS Apple silicon:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh | sh
```

The installer downloads the matching GitHub release archive, verifies its
SHA-256 checksum, and installs five commands — `evener`, `evener-hub`,
`evener-tui`, `evener-doctor`, and `evener-migrate` — under
`~/.local/share/evener/bin`, symlinked into `~/.local/bin`. Add
`~/.local/bin` to your `PATH` if your shell does not already find the
commands there.

Four environment variables adjust the install:

- `EVENER_INSTALL_VERSION=v1.2.3` installs a specific tagged release;
  `EVENER_INSTALL_VERSION=snapshot` installs the latest successful build from
  `main`.
- `PREFIX=/usr/local` changes the install root (use `sudo` for system-owned
  paths); `BINDIR` and `EVENER_SHARE_BINDIR` override the two install
  directories individually.

Pass installer variables to `sh`, not to `curl`, in a pipeline:

```bash
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh \
  | EVENER_INSTALL_VERSION=snapshot sh
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh \
  | sudo env PREFIX=/usr/local sh
```

From a source checkout, `make install` builds and installs the same layout;
`sudo make install-system` uses `/usr/local`. Runtime and config directories
appear on first run, not at install time.

Verify the install:

```bash
evener --version
```

## Start the hub

Run the hub in a terminal you can leave open:

```bash
evener-hub
```

The hub binds to loopback and listens on `127.0.0.1:9180` by default; a lock
file keeps it to one instance per hub state root. Keep the hub running —
sessions you start from the browser live behind it. To bind another address,
run under a supervisor, or deploy on a remote host, see the
[hub runbook](evener-hub.md) and
[docs/evener-hub-remote-operations.md](evener-hub-remote-operations.md).

The hub guards the web UI with a token. At startup it prints an authorization
URL:

```
[hub] auth URL (visit once per browser): http://127.0.0.1:9180/auth?token=...
```

Open that URL. It sets a long-lived cookie, and later visits to
`http://127.0.0.1:9180` need no token. The token also lives at
`${XDG_STATE_HOME:-$HOME/.local/state}/evener/auth-token` for scripted clients,
which pass it as `Authorization: Bearer`.

## Add provider credentials

Hosted LLM providers generally need a credential. Local/auth-none providers
such as Ollama can run without one. For providers that need credentials, the
web UI is the easiest place to add one: open
`http://127.0.0.1:9180/credentials` and paste an API key. The page writes
`${XDG_CONFIG_HOME:-$HOME/.config}/evener/credentials.toml` with owner-only
permissions. If `EVENER_PROVIDERS_CONFIG` points to a custom `providers.toml`,
Evener stores `credentials.toml` beside it.

Two alternatives cover other workflows. Environment variables such as
`OPENAI_API_KEY` and `ANTHROPIC_API_KEY` work as a fallback when the
credentials file has no entry for that provider. For OpenAI you can also sign
in with OAuth from the credentials page instead of managing a key. For the
full resolution order and provider-specific behavior, see
[docs/llm-provider-config-and-launch.md](llm-provider-config-and-launch.md).

## Your first session

Start a session from the browser. Open `http://127.0.0.1:9180/new`, type a
prompt, pick a model and a working directory, and click Start. The agent
begins working in that directory: reading files, running commands, and
editing code while the transcript streams live.

Steer the session as it runs. Send follow-up messages in the conversation,
or interrupt when it heads the wrong way. The sidebar's Live section sorts
running sessions by which one needs you first, excluding archived running
sessions, so several sessions can run in parallel without losing track of any
of them.

## Everyday workflow

A few concepts carry most of the interactive workflow:

- **Transparent resume.** Click any closed session, type, and send. The
  session continues from where it left off with the same identity.
- **Fork from here.** Every message you sent carries a fork button. Click it
  to branch the session at that point; the original message lands in the
  composer for you to edit, and the original line stays as a sibling fork in
  the sidebar.
- **Aside.** The *Aside: fork to side thread* palette command forks the
  current session into a side thread, so a tangential question does not
  derail the main line of work.
- **Search.** Press ⌘K (Ctrl+K on Linux) to search across live and past
  sessions.
- **Settings.** The settings page controls theme and notifications and shows
  provider and MCP configuration.

## Keep Evener current

Upgrade the installed binaries with:

```bash
evener upgrade
```

The command updates `evener`, `evener-hub`, `evener-tui`, and `evener-doctor`,
following the binary's install channel: release builds upgrade to the latest
release, snapshot builds to the latest `main` build. Pass `release`, `snapshot`,
or a tag such as `v1.2.3` to switch tracks. It does not update `evener-migrate`;
rerun the installer, or run `make install` (or `sudo make install-system`), to
refresh that command. The web UI's `/upgrade` command and the TUI call through
the same mechanism.

Running sessions keep the binary they started with. End a session, upgrade,
and resume it to pick up the new build.

## Other ways to drive Evener

- **`evener-tui`** is a terminal dashboard backed by the same hub: browse
  sessions, read transcripts, and send actions without a browser. See the
  [TUI section of the README](../README.md#evener-tui-terminal-user-interface).
- **`evener --model <provider/model> "<prompt>"`** runs one non-interactive
  session from the shell, suitable for scripts and CI. See
  [Non-interactive CLI](../README.md#non-interactive-cli).
- **`llmcall`** makes a single raw LLM call with no agent loop. See the
  [llmcall section of the README](../README.md#llmcall-one-shot-llm-client).
- **`evener-doctor`** inspects sessions, jobs, and watches when something
  misbehaves. Run `evener-doctor --help`.

## Next steps

- [docs/llm-providers.md](llm-providers.md) — supported providers and models,
  including local models through [Ollama](ollama.md).
- [docs/sandboxing.md](sandboxing.md) — confine a session's file, process,
  and network access.
- [docs/skills.md](skills.md) — extend Evener with skills.
- [docs/evener-hub.md](evener-hub.md) — production-style hub setup, launch
  configuration layering, and smoke checks.
- [docs/developing-evener/README.md](developing-evener/README.md) — building
  and testing Evener itself.
