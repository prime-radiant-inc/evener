# Evener documentation

Put a coding task in Evener's hands without losing sight of what changed or how it was checked. Start with a small, disposable project, then use the same pattern in your own repository: state the goal, follow the work, and verify the result.

**New here? [Get started](getting-started.md)** takes you from installation and provider setup to a repaired program with an independent acceptance check. You need basic terminal and Git knowledge, not previous experience with coding agents.

This documentation follows **main**, not a fixed release. Use a snapshot, the latest successful build from main, for the closest match. The [installation guide](getting-started.md#install-evener) explains the release alternative.

## Start

- [Get started](getting-started.md): install, authenticate, and finish your first task.
- [How Evener works](concepts.md): the few concepts behind the interface.

## Use Evener

- [Daily work](daily-work.md): send and steer requests, manage parallel work, resume, fork, and review.
- [Native app](native-app.md): obtain an iPhone build and connect it to your hub.
- [Device pairing](device-pairing.md): authorize a device to connect to your hub.
- [Memory](memory.md): retain useful lessons, correct them, and understand their scope.

## Configure

- [Connect a provider](connecting-a-provider.md): configure and check a model connection.
- [Environment variables](environment-variables.md): set the execution host's process environment.
- [Provider configuration and launch](llm-provider-config-and-launch.md): resolve provider instances and launch settings.
- [Configuration](configuration.md): providers, credentials, scope, and trust.
- [Model providers](llm-providers.md) and [Ollama for local models without a hosted-provider key](ollama.md): choose where to run your model.
- [Sandboxing](sandboxing.md): understand file, process, and network boundaries.

## Extend

[Extend Evener](extensions.md) helps you choose between reusable instructions, plugins, hooks, and external tools. Follow its links to the owning format references rather than copying an example for the wrong extension type.

## Automate and integrate

- [CLI reference](cli.md): commands, options, and output contracts.
- [Automation](automation.md): scripting recipes.
- [Integrations](integrations.md): connect a client and understand its lifecycle.
- [Subagent runtime contracts](subagent-runtime-contracts.md): understand delegate lifecycle and execution rules.
- [AppWire protocol](appwire-protocol.md): the exact client/server contract.

## Operate and troubleshoot

- [Security](security.md): review trust boundaries, credentials, and backups.
- [Troubleshooting](troubleshooting.md): start with the symptom you see.
- [Hub runbook](evener-hub.md): run the service and understand its configuration.
- [Remote operations](evener-hub-remote-operations.md): work across hosts.
- [Job control](job-control.md): exact delegate, shell-job, and watch contracts.

## Develop Evener

The [contributor guide](developing-evener/README.md) covers building and testing the product. The [subsystem map](product/subsystems.md) points to source owners; it is an implementation reference, not an additional setup guide.
