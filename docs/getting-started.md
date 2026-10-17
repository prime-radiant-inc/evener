# Get started

Finish your first Evener task by repairing a tiny Python program and checking the result yourself. You will install Evener, connect a model provider, and work in a disposable Git repository rather than in your real project.

Evener runs the agent on your computer or another configured host. The browser is its control surface. A **hub** serves that interface and connects it to sessions; a **session** holds one conversation and its work.

A **model provider** supplies the AI model that reads your request and proposes actions. Evener carries out those actions through **tools**, such as file readers and shell commands. A model with tool-call support can request those tools, rather than only return text.

## Before you begin

You need:

- Linux x64 or macOS on Apple silicon for the prebuilt installer below.
- A terminal with `sh`, `curl`, `tar`, `install`, and either `sha256sum` or `shasum`.
- Git and Python 3 for this tutorial's small example. Run `git --version` and `python3 --version` to check.
- A browser and network access to GitHub and your chosen model provider.
- An API key from a supported provider, or another supported authentication method. Hosted model use may incur charges.

If you want to run the model locally, configure [Ollama](ollama.md) instead of the hosted-provider step below. Complete its setup and choose a model that supports tool calls before starting the session. A local server does not need a hosted-provider key, but it does need a downloaded model and enough machine resources to run it.

Evener can edit files and run commands. This tutorial isolates the files you give it, but a disposable directory is **not a security sandbox**. Read [sandboxing](sandboxing.md) before working with untrusted code.

## Install Evener

These docs track **main**. Install the latest successful snapshot build for the closest match; it may still trail the newest source commit. Start in a new directory so the download cannot overwrite an earlier installer. Run this in your terminal:

```sh
mkdir evener-install-review && cd evener-install-review
```

If that directory already exists, choose another unused name before continuing. Then download and inspect the installer:

```sh
curl -fsSL https://raw.githubusercontent.com/prime-radiant-inc/evener/main/install.sh -o evener-install.sh
less evener-install.sh
EVENER_INSTALL_VERSION=snapshot sh evener-install.sh
```

Inspect the downloaded script before running it: it installs executable code. The installer verifies the archive's SHA-256 checksum, installs the `evener` executable under `~/.local/share/evener/bin`, and links it into `~/.local/bin` by default. Commands such as `evener hub` are subcommands of that executable.

Check that your shell can find Evener:

```sh
export PATH="$HOME/.local/bin:$PATH"
evener --version
```

Add that `PATH` entry to your shell's startup file if needed. The version command should print a version rather than `command not found`. If the installer rejects your platform, use the [source-build guide](developing-evener/README.md); changing the archive name cannot make a binary compatible.

### Release alternative

To choose the latest tagged release instead, run the same reviewed installer without the snapshot setting:

```sh
EVENER_INSTALL_VERSION=latest sh evener-install.sh
```

Release behavior can lag these guides. `EVENER_INSTALL_VERSION` also accepts an existing release tag. Do not treat an example version number as an available release. See the [CLI reference](cli.md) for upgrades and remote installation.

## Open the browser interface

Start the hub in a terminal you can leave open:

```sh
evener hub
```

By default it listens on `127.0.0.1:9180`, reachable only from the same computer. Open the authorization URL printed at startup. It has this shape, with a real token in place of the placeholder:

```text
http://127.0.0.1:9180/auth/<token>
```

The URL signs this browser in. Treat it as a credential: do not paste it into a conversation, screenshot, or bug report. After signing in, use `http://127.0.0.1:9180` normally.

Leave the hub running. Closing a browser tab is not a stop command for the agent. If the port is occupied or the page will not open, use [troubleshooting](troubleshooting.md) rather than launching more hub instances. For another bind address or a supervised service, use the [hub runbook](evener-hub.md).

## Connect a provider before starting work

This walkthrough uses Settings. The [guided Connect provider dialog](connecting-a-provider.md#start-your-first-session) offers another route to the same host's provider connections and credentials. If you already connected there, choose that connection rather than entering its credentials again.

In the authenticated browser, open **Settings → Providers & credentials**, at `/settings/credentials` on your hub. For a straightforward hosted setup:

1. Obtain an API key from your provider's account dashboard. An ordinary chat subscription is not necessarily an API account.
2. Select the matching configured provider entry, such as `openai` or `anthropic`, and save the key in **Providers & credentials**.
3. Open **New session** at `/new` and choose an available model from that provider. Do not submit a task yet.

For a supported ChatGPT/Codex subscription, the **Providers & credentials** page also offers OAuth sign-in for `openai-codex`. That is a separate configured provider entry from `openai`; saving an OpenAI API key is not the same action.

The default credential file is `~/.config/evener/credentials.toml` (under `XDG_CONFIG_HOME` when set). Evener writes it with owner-only permissions. Do not put keys in your project or prompt. Custom paths, environment-variable fallbacks, and provider-specific rules are covered in [configuration](configuration.md).

If the model you need is missing, check provider setup before continuing. A visible model choice is not proof that your account has quota or access; the first request can still return an authentication, billing, or model-access error.

## Prepare the example

The fixture is a short `total_cents` function. It should multiply each item's price by its quantity, then add the amounts. It currently ignores quantities.

In a separate terminal, choose a parent directory where neither `evener-docs` nor `evener-first-task` exists. Run:

```sh
git clone --depth 1 https://github.com/prime-radiant-inc/evener.git evener-docs
mkdir evener-first-task
cp evener-docs/docs/examples/first-task/README.md evener-first-task/
cp evener-docs/docs/examples/first-task/receipt.py evener-first-task/
cp evener-docs/docs/examples/first-task/check.py evener-first-task/
cd evener-first-task
git init
git add README.md receipt.py check.py
git -c user.name='Tutorial' -c user.email='tutorial@example.invalid' commit -m 'Add first-task fixture'
pwd
python3 check.py
```

The `git -c` options set a throwaway author identity for this one commit; they do not change your global Git settings.

Copy the absolute path printed by `pwd`; you will use it as the session's working directory. The final command **should fail**, exiting 1 with these three lines:

```text
FAIL: quantity: expected 750, got 250
FAIL: mixed: expected 490, got 205
FAIL: zero quantity: expected 100, got 1049
```

These failures establish the bug before the agent changes anything. If Python cannot run, fix that prerequisite first. If the check already passes, use a fresh copy of the fixture.

The checker uses only Python's standard library. It makes no model request and needs no Evener credentials. Read `check.py`: its expected answers are fixed examples, not values calculated by the function under test.

## Ask Evener to repair it

Return to `/new` in the browser. Select the provider model you configured and set the working directory to the absolute `evener-first-task` path. Review the launch settings, including any access or project-trust choices, before starting.

Paste the following into the **agent prompt**, not your terminal:

```text
Fix total_cents in receipt.py so each item's price is multiplied by its
quantity. Work only in this directory. Read README.md and check.py first.
Run python3 check.py to reproduce the bug, then make the smallest repair
in receipt.py and rerun the check. Do not change check.py or README.md.
Do not commit. Report what changed and the actual check result.
```

Start the session. The conversation shows the agent's messages and tool calls. Watch for the initial failure, the edit to `receipt.py`, and the rerun. If the agent proposes unrelated work, correct it in the composer. [Daily work](daily-work.md) explains Send, Steer, and Stop.

## Check the result yourself

When the agent reports completion, run these commands in your tutorial terminal:

```sh
python3 check.py
git diff -- receipt.py
git diff --exit-code -- README.md check.py
git status --short
```

Success means:

- The checker prints `PASS: 4 receipt cases` and exits zero.
- The diff fixes quantity handling in `receipt.py` without unrelated edits.
- The protected-file diff is empty and exits zero.
- The status lists only changes you understand. Python may have created an untracked `__pycache__/` directory.

The checker tests an empty receipt, one item with a quantity, mixed items, and a zero quantity. It does not prove every possible input works. The fixture's README defines the supported input; no validation feature is needed.

If a check fails, send the exact failure back to the same session and ask for a focused repair. If a protected file changed, inspect the diff before restoring it from your tutorial commit. Do not accept a passing check obtained by weakening the checker.

You have now completed the basic loop: give a bounded task, observe the work, and verify it independently. You can commit the reviewed repair in this disposable repository, or leave it for comparison.

## Move to your own project

Start with a clean Git working tree, a small task, and a test command you already trust. Tell Evener which files it may change and what it must leave alone. Keep deployment, credential changes, and destructive operations outside a first experiment.

Continue with [daily work](daily-work.md) for steering and review, [concepts](concepts.md) for session lifetimes, or [the CLI](cli.md) for noninteractive tasks. The [native app guide](native-app.md) explains how an iPhone connects to the same hub.
