//go:build serffuzz

package schema

import (
	"fmt"
	"strings"
	"testing"
)

// FuzzSessionDisplayName drives the label a session wears everywhere a human
// looks at one. Every input is model- or user-authored and none is validated:
// a name typed into the UI, the original prompt, and the session ID.
//
// The oracles are the user-visible properties rather than a restatement of the
// precedence chain:
//
//   - The result is always trimmed. It is rendered into lists and tab titles,
//     where leading whitespace silently misaligns a row and a whitespace-only
//     name reads as a blank session.
//   - A session that has any usable label never displays as nothing.
//   - A named session never falls back to its opaque ID. That fallback exists
//     for unnamed sessions, and reaching it for a named one is the visible bug.
func FuzzSessionDisplayName(f *testing.F) {
	f.Add("My session", "do the thing", "sess-1")
	f.Add("   ", "do the thing", "sess-1")
	f.Add("", "", "sess-1")
	f.Add("", "   ", "   ")
	f.Add("\t\n", "\n", "\t")
	f.Add("name", "", "")

	f.Fuzz(func(t *testing.T, name, prompt, id string) {
		if len(name)+len(prompt)+len(id) > 8192 {
			t.Skip()
		}
		meta := SessionMeta{Name: name, OriginalPrompt: prompt, ID: id}
		got := SessionDisplayName(meta)

		if got != strings.TrimSpace(got) {
			t.Fatalf("SessionDisplayName returned untrimmed %q", got)
		}

		anyUsable := strings.TrimSpace(name) != "" ||
			strings.TrimSpace(prompt) != "" ||
			strings.TrimSpace(id) != ""
		if anyUsable && got == "" {
			t.Fatalf("all three labels blank-checked usable but display name is empty (name=%q prompt=%q id=%q)", name, prompt, id)
		}
		if !anyUsable && got != "" {
			t.Fatalf("no usable label, but display name is %q", got)
		}

		if trimmedName := strings.TrimSpace(name); trimmedName != "" {
			if got != trimmedName {
				t.Fatalf("named session displays as %q, want its name %q", got, trimmedName)
			}
		}
	})
}

// FuzzHookAnnouncement drives the one-line summary a hook run announces. Its
// fields come from plugin-authored configuration, so any of them can be blank
// or whitespace.
//
// The properties that matter are formatting ones a naive Join breaks: a blank
// field must be DROPPED rather than rendered as a gap, and the exit status must
// always be present, because that is the part a reader is looking for.
func FuzzHookAnnouncement(f *testing.F) {
	f.Add("PreToolUse", "plugin", "matcher", "command", 0)
	f.Add("", "", "", "", 1)
	f.Add("  ", " plugin ", "", "\t", -1)
	f.Add("PostToolUse", "", "m", "", 137)

	f.Fuzz(func(t *testing.T, event, plugin, matcher, hookType string, exitCode int) {
		if len(event)+len(plugin)+len(matcher)+len(hookType) > 4096 {
			t.Skip()
		}
		info := HookInfo{
			Event:      event,
			PluginName: plugin,
			Matcher:    matcher,
			HookType:   hookType,
			ExitCode:   exitCode,
		}
		got := info.Announcement()

		if !strings.Contains(got, "hook") {
			t.Fatalf("announcement %q does not identify itself as a hook", got)
		}
		wantExit := fmt.Sprintf("exit %d", exitCode)
		if !strings.HasSuffix(got, wantExit) {
			t.Fatalf("announcement %q does not end with %q", got, wantExit)
		}
		// A dropped field must leave no trace. Two spaces means an empty field
		// was joined in anyway, which reads as a missing value rather than an
		// absent one. A field carrying its own internal run of spaces would
		// produce the same shape honestly, so that case is excluded rather than
		// reported as a formatting bug.
		internalRun := false
		for _, field := range []string{event, plugin, matcher, hookType} {
			if strings.Contains(strings.TrimSpace(field), "  ") {
				internalRun = true
			}
		}
		if !internalRun && strings.Contains(got, "  ") {
			t.Fatalf("announcement %q contains a doubled space, so a blank field was rendered", got)
		}
		if got != strings.TrimSpace(got) {
			t.Fatalf("announcement %q is not trimmed", got)
		}
		for _, field := range []string{plugin, matcher, hookType} {
			if v := strings.TrimSpace(field); v != "" && !strings.Contains(got, v) {
				t.Fatalf("announcement %q dropped the non-blank field %q", got, v)
			}
		}
	})
}
