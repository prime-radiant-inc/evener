# Get started

Finish your first Evener task in a project you already know. You will install
Evener, connect a model provider without leaving the New session form, choose a
model explicitly, review the session's effective permissions, and check the
result yourself. A [disposable Python exercise](#optional-disposable-exercise)
is available if you do not yet have a suitable project.

Evener runs the agent on your computer or another configured host. The browser is its control surface. A **hub** serves that interface and connects it to sessions; a **session** holds one conversation and its work.

A **model provider** supplies the AI model that reads your request and proposes actions. Evener carries out those actions through **tools**, such as file readers and shell commands. A model with tool-call support can request those tools, rather than only return text.

## Before you begin

You need:

- Linux x64 or macOS on Apple silicon for the prebuilt installer below.
- A terminal with `sh`, `curl`, `tar`, `install`, and either `sha256sum` or `shasum`.
- Git for the first-task review loop. Python 3 is needed only for the optional
  disposable exercise.
- A browser and network access to GitHub and your chosen model provider.
- An API key from a supported provider, or another supported authentication method. Hosted model use may incur charges.

If you want to run the model locally, configure [Ollama](ollama.md) instead of the hosted-provider step below. Complete its setup and choose a model that supports tool calls before starting the session. A local server does not need a hosted-provider key, but it does need a downloaded model and enough machine resources to run it.

Evener can edit files and run commands. Start with a project and task you can
review. A disposable directory is **not a security sandbox**. Read
[sandboxing](sandboxing.md) before working with untrusted code.

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

## Prepare a first task in your project

Choose a project you understand and a small, reversible task. Good first tasks
have a clean Git working tree, a narrow expected diff, and a check you already
trust. A documentation correction, a focused test repair, or a small bug with a
reproduction is better than deployment, credential changes, migrations, or
destructive work.

In the project's terminal, record the starting point:

```sh
git status --short
pwd
```

Run the task's check before Evener changes anything. Keep its command and output;
you will run the same check afterward. If the tree is not clean, either account
for every existing change or choose another project. Evener does not decide
which pre-existing changes are safe to overwrite.

## Connect a provider in New session

Open **New session** at `/new`. The working directory, task draft, provider, and
model all belong to this launch form:

1. Choose the project directory reported by `pwd` and write your bounded task.
   Your directory and task remain in the form while provider setup opens and
   closes.
2. If the form says a provider is required, select **Connect provider**. Choose
   the provider you intend to use. An ordinary chat subscription is not
   necessarily an API account.
3. For an API connection, paste the actual key into the masked field and select
   **Save and check**. For a supported ChatGPT/Codex subscription, choose the
   separate **OpenAI Codex** sign-in route instead; it is not the `openai` API-key
   connection.
4. A successful check means Evener saved the resolved access and fetched that
   provider connection's model list. It does **not** send your task, make a
   generation request, or prove quota, billing, model capability, or generation
   access. Fix an inline failure and retry; a failed list check does not erase a
   key that was saved successfully.
5. Select **Choose model**, then explicitly choose the model for this session.
   If listing fails, use **Retry models**. If the provider cannot support a list
   check, **Continue without verification** is an unverified route, not a pass.

The default credential file is `~/.config/evener/credentials.toml` (under
`XDG_CONFIG_HOME` when set). Evener writes it with owner-only permissions. Do not
put keys in your project, task, screenshot, or bug report. Custom paths,
environment fallbacks, and provider-specific rules are covered in
[configuration](configuration.md) and [Connecting a provider](connecting-a-provider.md).

## Review the task and permissions, then Start

Before selecting **Start**, confirm the directory, task, and chosen model are
still the ones you intended. Review **Effective access** near Start. It reports
the resolved launch setting, including an Advanced sandbox override when one is
set. Use **Change** to reach the control that owns that value.

Task instructions and permissions are different controls. A sentence such as
“work only in this directory” tells the model what you want; it does not restrict
the tools. **Full access** has no sandbox restriction. **Read-only** prevents
workspace changes. **Workspace write** permits changes inside the workspace.
**Restricted** applies the tightest filesystem and command access. Use the least
access the task needs, and treat the displayed Effective access value as the
launch result rather than inferring access from the task's wording.

Use a task shaped like this, replacing every placeholder with facts from your
project:

```text
Fix <one specific problem>. First run <trusted check> to reproduce it.
You may change <named files or area>. Do not change <protected files or area>.
Make the smallest repair, rerun <trusted check>, and report the changed files
and the actual result. Do not commit or deploy.
```

Those file instructions bound the requested work; the access setting bounds the
tools. Select **Start** only after both are right. Connecting a provider and
choosing a model do not create a session. Leaving the task blank is also
supported: Start creates an empty session where you can write the task later.

The conversation shows the agent's messages and tool calls. If it proposes
unrelated work, correct it in the composer. [Daily work](daily-work.md) explains
Send, Steer, and Stop.

## Check the result yourself

When the agent reports completion:

1. Run the same check you recorded before the session.
2. Read `git diff` and `git status --short`.
3. Confirm protected files did not change and every remaining change belongs to
   the task.

The agent's report is evidence to inspect, not the final verdict. Do not accept a
passing check obtained by weakening that check. If a check fails, send its exact
output back to the same session and ask for a focused repair.

You have now completed the basic loop: state a bounded task, choose its actual
permissions, observe the work, and verify it independently.

## Optional disposable exercise

Use this exercise if you do not have a suitable project. It copies a small
`total_cents` bug and an independent checker into a disposable Git repository:

Choose a parent directory where neither `evener-docs` nor `evener-first-task`
exists. Run this whole block together. The checks and `&&` chain stop setup if
either path already exists or any setup command fails; do not continue by
running the copy commands separately.

```sh
[ ! -e evener-docs ] && [ ! -L evener-docs ] &&
[ ! -e evener-first-task ] && [ ! -L evener-first-task ] &&
mkdir evener-docs evener-first-task &&
git clone --depth 1 https://github.com/prime-radiant-inc/evener.git evener-docs &&
cp evener-docs/docs/examples/first-task/README.md evener-first-task/ &&
cp evener-docs/docs/examples/first-task/receipt.py evener-first-task/ &&
cp evener-docs/docs/examples/first-task/check.py evener-first-task/ &&
cd evener-first-task &&
git init &&
git add README.md receipt.py check.py &&
git -c user.name='Tutorial' -c user.email='tutorial@example.invalid' commit -m 'Add first-task fixture' &&
pwd &&
python3 check.py
```

The check should exit 1 because `receipt.py` ignores quantities. Use the printed
directory in New session, choose **Workspace write**, and give this task:

```text
Fix total_cents in receipt.py so each item's price is multiplied by its
quantity. Read README.md and check.py first. Run python3 check.py to reproduce
the bug, then make the smallest repair in receipt.py and rerun the check. Do not
change check.py or README.md. Do not commit. Report the diff and check result.
```

Afterward, run:

```sh
python3 check.py
git diff -- receipt.py
git diff --exit-code -- README.md check.py
git status --short
```

Success is `PASS: 4 receipt cases`, a focused `receipt.py` diff, no protected-file
diff, and only changes you understand. The checker covers four fixed examples,
not every possible input. A disposable directory still is not a security sandbox.

Continue with [daily work](daily-work.md) for steering and review,
[concepts](concepts.md) for session lifetimes, or [the CLI](cli.md) for
noninteractive tasks. The [native app guide](native-app.md) explains how an
iPhone connects to the same hub.
