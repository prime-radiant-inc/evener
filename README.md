# Evener

Evener is a coding agent that works in your project: it reads files, runs commands, edits code, and checks the result. Use the browser to follow and steer its work, the CLI to run a task from a script, or the iPhone client to reach your hub away from your desk. The hub is the service that connects these interfaces to your sessions.

**[Get started](docs/getting-started.md)** to install Evener, connect a model provider, and repair a small program with a check you can run yourself.

These guides track **main**. The first-run guide uses the latest successful snapshot from main; tagged releases can lag the behavior described here.

Start the service with `evener hub`. Its listener (`--addr`, `EVENER_HUB_LISTEN_ADDR`, or TOML `addr`), a client's destination (`--hub-url` or `EVENER_HUB_URL` where supported), and the advertised `public_url` are separate settings. See [hub endpoints](docs/evener-hub-endpoints.md) for discovery and migration.

## Find your next step

| You want to… | Read |
| --- | --- |
| Install and finish a first task | [Get started](docs/getting-started.md) |
| Steer work, resume a session, or review changes | [Daily work](docs/daily-work.md) |
| Understand sessions, delegates, jobs, and memory | [How Evener works](docs/concepts.md) |
| Choose models and configure access | [Configuration](docs/configuration.md) |
| Add reusable instructions or tools | [Extend Evener](docs/extensions.md) |
| Run Evener from a script | [CLI reference](docs/cli.md) |
| Connect a client | [Integrations](docs/integrations.md) |
| Connect an iPhone | [Native app](docs/native-app.md) |
| Diagnose a problem | [Troubleshooting](docs/troubleshooting.md) |
| Upgrade an existing install | [Upgrading](UPGRADING.md) |

The [documentation home](docs/index.md) brings these paths together. The [published guide](https://prime-radiant-inc.github.io/evener/) uses the same Markdown sources.

## Work with evidence

Give Evener a goal, boundaries, and a way to check success. Read its changes and rerun the relevant checks before accepting the result. A model's summary is not a test result, and stopping a session does not undo its edits.

Evener can send project content to your selected model provider and execute tools on the session's host. Review [sandboxing](docs/sandboxing.md) before using an unfamiliar repository or granting broader access. Keep credentials out of prompts and source files.

## Contribute

For source builds, architecture, and test requirements, start with [Developing Evener](docs/developing-evener/README.md). [The subsystem map](docs/product/subsystems.md) connects product behavior to its implementation. User documentation lives in `docs/`; implementation proposals and historical records are not promises of shipped behavior.
