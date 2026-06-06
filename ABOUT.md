# serf

> A non-interactive coding agent: give it a prompt and it reads, writes, runs commands, and searches code in a loop until the work is done.

**Family:** agent-libs · **Type:** tool · **Lifecycle:** production · **Owner:** obra

## What it does
Serv is a non-interactive coding agent. Given a prompt, it uses the LLM's native tool-calling to read files, write files, run commands, and search code in a loop until the work is complete. It supports OpenAI, Anthropic, and Google models. The repo also ships `llmcall` (a one-shot LLM client) and a multi-session web orchestrator (`hub`).

## How it fits
- Depends on: — (Go workspace of intra-repo modules only: serf/agent, serf/auth, serf/llm; no internal prime-radiant-inc cross-repo deps)
- Used by: —
- External: OpenAI, Anthropic, and Google LLM APIs (native tool-calling)

## Runtime & data
- Runs: CLI (`serf`, `llmcall`) plus an optional web orchestrator (`hub`)
- Data in: prompt (args or stdin), local filesystem, shell command output
- Data out: file edits, command execution, agent transcript

<!-- Maintained by the maintaining-project-map skill. Do not hand-edit; regenerated. -->
