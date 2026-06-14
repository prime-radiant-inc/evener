// Package kimicoding holds constants shared by the Kimi coding-plan provider
// adapters (kimi over the OpenAI route and kimi-anthropic over the Anthropic
// route).
package kimicoding

// UserAgent is the User-Agent the Kimi coding-plan adapters announce. Kimi For
// Coding gates its endpoints behind a coding-agent User-Agent allowlist
// (Kimi CLI, Claude Code, Roo Code, Kilo Code, …) and rejects others with a 403.
// This matches Claude Code's "claude-cli/<version> (external, cli)" format, which
// the gate accepts. Bump the version to track the Claude Code release in use.
const UserAgent = "claude-cli/2.1.177 (external, cli)"
