// Package diagnostic answers one question about a failure — "whose fault was
// this, and what should the user do about it?" — and answers it identically for
// every surface that asks.
//
// It is public, and lives in the agent module, because both sides of a
// diagnostic's life need it: the agent stamps a Source onto the events.WarningData
// and events.ErrorData it emits, and the hub, CLI, and TUI re-derive Title and
// Hint from those same fields when projecting them for display. A second copy
// in the root module would let those two answers drift apart, so the classifier
// that stamps an event and the classifier that renders it are the same code.
package diagnostic

import (
	"errors"
	"regexp"
	"strings"

	"primeradiant.com/serf/llm"
)

// Source names the component a diagnostic is attributed to.
type Source string

// The sources a diagnostic may be attributed to. A Source that is stamped by
// one surface must be recognized by every other, or attribution is lost when
// the diagnostic is re-derived downstream.
const (
	SourceProvider Source = "provider"
	SourceSerf     Source = "serf"
	SourceHub      Source = "hub"
	SourceUI       Source = "ui"
	SourceHook     Source = "hook"
	SourceMCP      Source = "mcp"
)

// Info is a classified diagnostic: who it came from, and the user-facing
// headline and guidance that go with it.
type Info struct {
	Source Source
	Title  string
	Hint   string
}

// Classify infers a diagnostic from a bare message by keyword.
func Classify(message string) Info {
	lower := strings.ToLower(strings.TrimSpace(message))
	switch {
	case isSerfConfiguration(lower):
		return serfConfiguration()
	case isHubFailure(lower):
		return hubFailure()
	// Checked ahead of the general provider case: an exhausted allowance needs
	// different guidance than a transient failure, and both are provider errors.
	case isUsageLimit(lower):
		return usageLimitFailure(message)
	case isProviderFailure(lower):
		return providerFailure()
	default:
		return serfFailure()
	}
}

// FromError classifies an error, preferring its structured category over the
// wording of its message.
func FromError(err error) Info {
	if err == nil {
		return serfFailure()
	}
	var cfg *llm.ConfigurationError
	if errors.As(err, &cfg) {
		return serfConfiguration()
	}
	// The typed check comes first: an exhausted allowance is recognized from the
	// error's own category rather than from wording that may vary by provider.
	if llm.Kind(err) == llm.KindQuotaExceeded {
		return usageLimitFailure(err.Error())
	}
	var llmErr llm.Error
	if errors.As(err, &llmErr) && (strings.TrimSpace(llmErr.Provider()) != "" || llmErr.StatusCode() != 0 || strings.TrimSpace(llmErr.ErrorCode()) != "") {
		return providerFailure()
	}
	return Classify(err.Error())
}

// FromFields re-derives a diagnostic from fields already carried on an event,
// filling in whatever the emitter left blank. A source it recognizes is
// authoritative and suppresses keyword classification; a title or hint the
// emitter supplied is preserved verbatim.
func FromFields(source, title, hint, message string) Info {
	info := Classify(message)
	if src := normalizeSource(source); src != "" {
		info = defaultForSource(src, message)
	}
	if strings.TrimSpace(title) != "" {
		info.Title = strings.TrimSpace(title)
	}
	if strings.TrimSpace(hint) != "" {
		info.Hint = strings.TrimSpace(hint)
	}
	return info
}

func normalizeSource(source string) Source {
	switch Source(strings.ToLower(strings.TrimSpace(source))) {
	case SourceProvider:
		return SourceProvider
	case SourceSerf:
		return SourceSerf
	case SourceHub:
		return SourceHub
	case SourceUI:
		return SourceUI
	case SourceHook:
		return SourceHook
	case SourceMCP:
		return SourceMCP
	default:
		return ""
	}
}

func defaultForSource(source Source, message string) Info {
	switch source {
	case SourceProvider:
		if isUsageLimit(strings.ToLower(strings.TrimSpace(message))) {
			return usageLimitFailure(message)
		}
		return providerFailure()
	case SourceHub:
		return hubFailure()
	case SourceUI:
		return Info{
			Source: SourceUI,
			Title:  "UI error",
			Hint:   "Check the browser console and UI state.",
		}
	case SourceSerf:
		if isSerfConfiguration(strings.ToLower(strings.TrimSpace(message))) {
			return serfConfiguration()
		}
		return serfFailure()
	case SourceHook:
		return Info{
			Source: SourceHook,
			Title:  "Hook message",
			Hint:   "A plugin hook returned a user-facing message.",
		}
	case SourceMCP:
		return mcpFailure()
	default:
		return Classify(message)
	}
}

func serfConfiguration() Info {
	return Info{
		Source: SourceSerf,
		Title:  "Serf configuration error",
		Hint:   "Hub launched Serf with provider configuration this Serf runtime does not recognize. Check the model/provider passed by Hub and the Serf binary Hub is using.",
	}
}

func providerFailure() Info {
	return Info{
		Source: SourceProvider,
		Title:  "Provider error",
		Hint:   "The model provider failed to complete the response. Check the selected model, credentials, account access, and rate limits. The daemon is fine — retrying the turn or switching models may help. Note: if an OpenAI model does not support the Responses API (/v1/responses), Serf automatically falls back to Chat Completions (/v1/chat/completions). If both fail, the error message names the model and both endpoints.",
	}
}

// usageLimitTitle names the exhausted-allowance case. It is deliberately
// distinct from "Provider error": the account is fine and the daemon is fine,
// the allowance is simply spent.
const usageLimitTitle = "Usage limit reached"

// providerStatusPrefix matches the rendering every llm.Error carries:
// "<provider> error (status=<code>): <detail>".
var providerStatusPrefix = regexp.MustCompile(`\berror \(status=\d+\)`)

// usageLimitFailure builds the guidance for an exhausted plan or quota. The
// reset window is already rendered into message by the llm layer (relative and
// absolute), so the hint repeats the message's tail rather than reformatting a
// time it would have to re-parse.
func usageLimitFailure(message string) Info {
	hint := "This account's model allowance is spent. Sending the turn again will fail the same way."
	if window := resetWindowFrom(message); window != "" {
		hint += " The allowance resets " + window + "."
	}
	hint += " Wait for the reset, or switch to a different provider instance or model."
	return Info{
		Source: SourceProvider,
		Title:  usageLimitTitle,
		Hint:   hint,
	}
}

// resetWindowFrom lifts the "resets in 3d 17h (Tue Jul 28 10:02 PDT)" clause out
// of an already-formatted usage-limit message, returning "" when absent.
func resetWindowFrom(message string) string {
	_, after, found := strings.Cut(message, "resets ")
	if !found {
		return ""
	}
	// The clause ends at the close of the parenthesized absolute time.
	if end := strings.Index(after, ")"); end >= 0 {
		return strings.TrimSpace(after[:end+1])
	}
	return ""
}

// isUsageLimit matches an exhausted allowance in an already-lowercased message,
// for the paths that only carry the rendered string (transcript projection, hub
// relays) and cannot inspect the error's category.
func isUsageLimit(message string) bool {
	return strings.Contains(message, "usage limit") ||
		strings.Contains(message, "usage_limit_reached") ||
		strings.Contains(message, "insufficient_quota")
}

func mcpFailure() Info {
	return Info{
		Source: SourceMCP,
		Title:  "MCP server error",
		Hint:   "An MCP server failed to connect, authenticate, or complete a tool call. Check the command is on PATH (stdio), the URL/headers and auth token (http/sse), and that the server speaks MCP. The session runs without it; other tools are unaffected.",
	}
}

func hubFailure() Info {
	return Info{
		Source: SourceHub,
		Title:  "Hub error",
		Hint:   "Check the hub process, AppWire connection, spawn arguments, and rendezvous state.",
	}
}

func serfFailure() Info {
	return Info{
		Source: SourceSerf,
		Title:  "Serf error",
		Hint:   "Check the Serf session log and daemon state.",
	}
}

func isSerfConfiguration(message string) bool {
	return strings.Contains(message, "unknown provider") ||
		strings.Contains(message, "configuration error") ||
		strings.Contains(message, "must use provider/model") ||
		strings.Contains(message, "no model:")
}

// HubFailureKeywords is the vocabulary of a hub failure: a daemon or session
// that went away, or the transport between them, where the honest recovery is
// to reconnect and re-issue the turn rather than to go read a log.
//
// It is exported and data-shaped because Go is not the only reader. The hub's
// web client runs the same vocabulary over the same messages to decide between
// "Retry" and "Reconnect & retry" on a failed turn
// (frontend/src/panes/session/transcript/turnFailure.ts), and the two lists had
// already drifted apart in both directions by the time anyone noticed. They are
// held together now by TestHubFailureKeywordsMatchWebClient in cmd/serf-hub.
//
// Every entry must be lowercase: isHubFailure matches against an
// already-lowercased message, so an uppercase keyword would never fire.
var HubFailureKeywords = []string{
	"rendezvous",
	"daemon spawn",
	"resume timed out",
	"process exited before rendezvous",
	"appwire",
	"websocket",
	"stream failed",
	"source not found",
	"local daemon unavailable",
	"session unavailable",
}

func isHubFailure(message string) bool {
	for _, keyword := range HubFailureKeywords {
		if strings.Contains(message, keyword) {
			return true
		}
	}
	return false
}

func isProviderFailure(message string) bool {
	// Structured llm.Errors are caught earlier by FromError; this function
	// handles keyword fallbacks for plain-string classification.
	//
	// The status-prefix check carries the cases the keyword list cannot: an
	// llm.Error renders as "<provider> error (status=<code>): <detail>", and
	// the detail is now the provider's own wording, which may contain none of
	// the keywords below.
	return providerStatusPrefix.MatchString(message) ||
		strings.Contains(message, "provider unavailable") ||
		strings.Contains(message, "api key") ||
		strings.Contains(message, "rate limit") ||
		strings.Contains(message, "quota") ||
		strings.Contains(message, "unauthorized") ||
		strings.Contains(message, "invalid_grant") ||
		strings.Contains(message, "token endpoint") ||
		// Stream-truncation patterns: the LLM provider closed the stream
		// without emitting a finish event or response. The previous
		// classifier mislabeled these as Serf errors and told users to
		// check the daemon — but the daemon is fine; the upstream API
		// stream is what failed.
		strings.Contains(message, "stream ended without") ||
		strings.Contains(message, "stream error") ||
		strings.Contains(message, "missing response in finish event")
}
