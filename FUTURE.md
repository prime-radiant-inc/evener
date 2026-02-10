# Future Work

Items that need to happen to the product but aren't being worked on right now.

## System prompts need to be richer

The system prompts for all three profiles are one-sentence stubs. They don't
cover multi-step task approach, error recovery, safety, when to use which tool,
or coding best practices. The spec says to mirror the reference agents' prompts
(codex-rs, Claude Code, gemini-cli) byte-for-byte. We're far from that.

## Recommended models in system prompt

The system prompt should describe recommended current models for various use
cases (e.g., cheap models for utility tasks, reasoning models for complex work).
This helps the agent make informed decisions when spawning sub-agents with model
overrides.

## MCP (Model Context Protocol) client

The spec (Section 8) flags MCP as a natural extension. The tool registry already
supports dynamic registration. MCP would let users plug in GitHub, Jira, Slack,
database tools, etc. Needs its own design.

## Sandbox / security policies

OS-level sandboxing (macOS Seatbelt, Linux Landlock) to constrain file and
network access. The ExecutionEnvironment abstraction provides a natural hook.

## Web search tool

Distinct from web_fetch. Would need an API key for Google/Bing/etc. Less
critical since the model can often work without it, but useful for finding
documentation and error solutions.
