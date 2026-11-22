package schema

import (
	"path/filepath"
	"strings"
)

// The render-failure diagnostic is prose, not a prompt. It is assembled from
// these halves so its readers can match the whole shape rather than its opening
// words: a custom prompt may legitimately start with "Template rendering
// failed:".
const (
	renderFailurePromptPrefix = "Template rendering failed: "
	renderFailurePromptSuffix = ". Please report this bug."
)

// ValidSavedSystemPrompt reports the prompt a saved field holds, or "" when
// what it holds is render-failure prose rather than a prompt. Such a field is
// no answer to "what did this session launch with", so every reader treats it
// as empty and renders a fresh one.
func ValidSavedSystemPrompt(p string) string {
	if strings.HasPrefix(p, renderFailurePromptPrefix) && strings.HasSuffix(p, renderFailurePromptSuffix) {
		return ""
	}
	return p
}

// RenderFailureSystemPrompt is the prose a failed render hands the model. It is
// the exact shape ValidSavedSystemPrompt rejects, so a saved field never
// replays it as a prompt.
func RenderFailureSystemPrompt(err error) string {
	return renderFailurePromptPrefix + err.Error() + renderFailurePromptSuffix
}

// LaunchSystemPromptBesideTranscript returns the launch prompt a saved session
// recorded in the meta beside its transcript, or "" when it recorded none (or
// what it recorded is render-failure prose). A transcript whose header carries
// its own prompt never needs this: only a fork that starts with an empty header
// — a role- or plugin-changing fork — saves the prompt it rendered to the meta.
func LaunchSystemPromptBesideTranscript(transcriptPath string) string {
	base := filepath.Base(transcriptPath)
	if !strings.HasSuffix(base, SessionTranscriptSuffix) {
		return ""
	}
	id := strings.TrimSuffix(base, SessionTranscriptSuffix)
	// <stateDir>/sessions/<id>.transcript.jsonl: the meta sits in the same
	// sessions directory, whose parent is the state directory.
	sessionsDir := filepath.Dir(transcriptPath)
	meta, err := LoadSessionMeta(filepath.Dir(sessionsDir), id)
	if err != nil {
		return ""
	}
	return ValidSavedSystemPrompt(meta.LaunchSystemPrompt)
}
