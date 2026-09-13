package tui

import (
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuipick"
)

// TestHubDetailFromThreadMapsSharedNotes asserts hubDetailFromThread carries
// thread.Evener.{HumanNote,AgentNote,SessionURLs} plus the SharedNotes
// capability bit onto hubSessionDetail verbatim, mirroring how Queue/Goal are
// already mapped. SharedNotes is NOT zeroed for non-live sessions: notes stay
// readable on ended sessions and only the edit/remove commands gate on it.
func TestHubDetailFromThreadMapsSharedNotes(t *testing.T) {
	thread := appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Source:    "local",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener: appwire.EvenerThread{
			Ref:       "local:th_1",
			HumanNote: "h",
			AgentNote: "a",
			SessionURLs: []appwire.SessionURL{
				{ID: "u1", URL: "https://x.test/"},
			},
			Capabilities: appwire.ThreadCapabilities{SharedNotes: true},
		},
	}

	detail := hubDetailFromThread(thread)

	if !detail.Capabilities.SharedNotes {
		t.Fatalf("Capabilities.SharedNotes = false, want true (detail = %+v)", detail)
	}
	if detail.HumanNote != "h" {
		t.Fatalf("HumanNote = %q, want %q", detail.HumanNote, "h")
	}
	if detail.AgentNote != "a" {
		t.Fatalf("AgentNote = %q, want %q", detail.AgentNote, "a")
	}
	if len(detail.SessionURLs) != 1 {
		t.Fatalf("SessionURLs = %+v, want one entry", detail.SessionURLs)
	}

	// Ended sessions keep the capability bit so the drawer still renders the
	// section read-only.
	thread.Status = appwire.ThreadStatus{Type: appwire.ThreadStatusClosed}
	ended := hubDetailFromThread(thread)
	if ended.Live {
		t.Fatalf("ended detail Live = true, want false (detail = %+v)", ended)
	}
	if !ended.Capabilities.SharedNotes {
		t.Fatalf("ended Capabilities.SharedNotes = false, want true (detail = %+v)", ended)
	}
	if ended.HumanNote != "h" || len(ended.SessionURLs) != 1 {
		t.Fatalf("ended notes lost: detail = %+v", ended)
	}
}

// TestDetailsDrawerSharedNotesSection asserts the drawer renders the Shared
// notes section per the ordered display rule: hidden without the capability,
// read views when set, the /notes ghost trigger only when live, and plain
// read-only text (no trigger) when ended-empty.
func TestDetailsDrawerSharedNotesSection(t *testing.T) {
	withTestColorProfile(t)
	strip := func(s string) string { return ansiPattern.ReplaceAllString(s, "") }

	// Capability unset: no section at all.
	got := strip(detailsDrawer{Detail: hubSessionDetail{
		Live:      true,
		HumanNote: "h",
	}}.View())
	if strings.Contains(got, "SHARED NOTES") {
		t.Fatalf("section rendered without the capability:\n%s", got)
	}

	// Capability set + live: notes, agent note, labeled link, no ghost.
	got = strip(detailsDrawer{Detail: hubSessionDetail{
		Live:      true,
		HumanNote: "h",
		AgentNote: "a",
		SessionURLs: []appwire.SessionURL{
			{ID: "u1", URL: "https://x.test/", Label: "why"},
		},
		Capabilities: hubSessionCapabilities{SharedNotes: true},
	}}.View())
	for _, want := range []string{"SHARED NOTES", "You:    h", "Agent:  a", "Link:   why (https://x.test/) [u1]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("drawer missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Add a note with /notes") {
		t.Fatalf("ghost trigger rendered with content:\n%s", got)
	}

	// Capability set + live + empty: ghost trigger.
	got = strip(detailsDrawer{Detail: hubSessionDetail{
		Live:         true,
		Capabilities: hubSessionCapabilities{SharedNotes: true},
	}}.View())
	if !strings.Contains(got, "SHARED NOTES") || !strings.Contains(got, "Add a note with /notes") {
		t.Fatalf("live-empty section missing ghost trigger:\n%s", got)
	}

	// Capability set + restart-required + empty: read-only copy, no trigger.
	got = strip(detailsDrawer{Detail: hubSessionDetail{
		Live:         true,
		State:        appwire.ThreadStatusRestartRequired,
		Capabilities: hubSessionCapabilities{SharedNotes: true},
	}}.View())
	if !strings.Contains(got, "No shared notes") {
		t.Fatalf("restart-required empty section missing inert copy:\n%s", got)
	}
	if strings.Contains(got, "Add a note with /notes") {
		t.Fatalf("restart-required empty section rendered the edit trigger:\n%s", got)
	}

	// Capability set + ended + empty: section renders inert text, no trigger.
	got = strip(detailsDrawer{Detail: hubSessionDetail{
		Capabilities: hubSessionCapabilities{SharedNotes: true},
	}}.View())
	if !strings.Contains(got, "SHARED NOTES") || !strings.Contains(got, "No shared notes") {
		t.Fatalf("ended-empty section missing inert copy:\n%s", got)
	}
	if strings.Contains(got, "Add a note with /notes") {
		t.Fatalf("ended-empty section rendered the live trigger:\n%s", got)
	}
}

// TestSharedNotesCommandUnavailableWhenRestartRequired pins the write half of
// the capability split: a restart-required session keeps SharedNotes readable,
// which must not advertise /notes (nor let it dispatch) while every mutation is
// refused.
func TestSharedNotesCommandUnavailableWhenRestartRequired(t *testing.T) {
	caps := hubSessionCapabilities{SharedNotes: true}
	var notes hubCommandDefinition
	for _, command := range hubCommandsForScope(hubCommandSession) {
		if command.Name == "notes" {
			notes = command
		}
	}
	if notes.Name == "" {
		t.Fatal("/notes command not registered")
	}
	if available, reason := hubCommandAvailable(notes, hubCommandContext{mode: hubModeSession, caps: caps, live: true, state: appwire.ThreadStatusIdle}); !available {
		t.Fatalf("live idle session did not advertise /notes: %s", reason)
	}
	if available, reason := hubCommandAvailable(notes, hubCommandContext{mode: hubModeSession, caps: caps, live: true, state: appwire.ThreadStatusRestartRequired}); available {
		t.Fatalf("restart-required session advertised /notes: %s", reason)
	}
}

// TestSharedNotesSurfacesThreadSessionState pins the write gate on the surfaces
// that build their own command context: a restart-required session keeps the
// read capability but refuses every mutation, so neither the command palette
// nor the slash help may offer /notes.
func TestSharedNotesSurfacesThreadSessionState(t *testing.T) {
	caps := hubSessionCapabilities{SharedNotes: true}

	notesEntry := func(state string) tuipick.PickerPanelItem {
		for _, entry := range commandPaletteEntriesForSession(hubModeSession, caps, true, state, nil) {
			if entry.Command == "notes" {
				return entry.Item
			}
		}
		t.Fatalf("palette omitted /notes (state=%q)", state)
		return tuipick.PickerPanelItem{}
	}
	if got := notesEntry(appwire.ThreadStatusIdle); got.DisabledReason != "" {
		t.Fatalf("idle palette disabled /notes: %q", got.DisabledReason)
	}
	if got := notesEntry(appwire.ThreadStatusRestartRequired); got.DisabledReason == "" {
		t.Fatal("restart-required palette offered /notes")
	}

	if !strings.Contains(hubSlashCommandHelpLive(caps, true, appwire.ThreadStatusIdle), "/notes") {
		t.Fatal("idle help omitted /notes")
	}
	if strings.Contains(hubSlashCommandHelpLive(caps, true, appwire.ThreadStatusRestartRequired), "/notes") {
		t.Fatal("restart-required help offered /notes")
	}
}

// TestSharedNotesPaletteThreadsSessionState drives the production palette open,
// which must read the session's own state rather than leaving it blank.
func TestSharedNotesPaletteThreadsSessionState(t *testing.T) {
	notesDisabled := func(state string) bool {
		m := newSessionHubModel(nil)
		m.mode = hubModeSession
		m.detail.Capabilities = hubSessionCapabilities{SharedNotes: true}
		m.detail.Live = true
		m.detail.State = state
		m.openCommandPalette()
		if m.commandPalette == nil {
			t.Fatal("palette not opened")
		}
		for _, entry := range m.commandPalette.entries {
			if entry.Command == "notes" {
				return entry.Item.DisabledReason != ""
			}
		}
		t.Fatalf("palette omitted /notes (state=%q)", state)
		return true
	}
	if notesDisabled(appwire.ThreadStatusIdle) {
		t.Fatal("idle session palette disabled /notes")
	}
	if !notesDisabled(appwire.ThreadStatusRestartRequired) {
		t.Fatal("restart-required session palette offered /notes")
	}
}

// TestSharedNotesPushesUpdateCachedDetail asserts the evener/notes/updated and
// evener/urls/updated pushes land on the cached session detail so the drawer
// re-renders, and that frames for other sessions are ignored.
func TestSharedNotesPushesUpdateCachedDetail(t *testing.T) {
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{SharedNotes: true}

	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyEvenerNotesUpdated, appwire.NotesUpdatedParams{
		ThreadID: "01SEND", Ref: "local:01SEND", HumanNote: "h", AgentNote: "a",
	}).Notification)
	if m.detail.HumanNote != "h" || m.detail.AgentNote != "a" {
		t.Fatalf("notes push not applied: detail = %+v", m.detail)
	}

	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyEvenerUrlsUpdated, appwire.UrlsUpdatedParams{
		ThreadID: "01SEND",
		Ref:      "local:01SEND",
		URLs:     []appwire.SessionURL{{ID: "u1", URL: "https://x.test/"}},
	}).Notification)
	if len(m.detail.SessionURLs) != 1 || m.detail.SessionURLs[0].ID != "u1" {
		t.Fatalf("urls push not applied: detail = %+v", m.detail)
	}

	// Other-session frames must not leak into this view.
	m.applyHubNotification(*appwire.NotificationMessage(appwire.NotifyEvenerNotesUpdated, appwire.NotesUpdatedParams{
		ThreadID: "01OTHER", Ref: "local:01OTHER", HumanNote: "nope",
	}).Notification)
	if m.detail.HumanNote != "h" {
		t.Fatalf("other-session notes push leaked: detail = %+v", m.detail)
	}
}

// TestSharedNotesCommandsGateOnCapability asserts the /notes and /url-remove
// commands are unavailable without the SharedNotes capability and advertised
// with it.
func TestSharedNotesCommandsGateOnCapability(t *testing.T) {
	for _, name := range []string{"notes", "url-remove"} {
		definition, ok := hubCommandByName(name)
		if !ok {
			t.Fatalf("/%s is not registered", name)
		}
		available, _ := hubCommandAvailable(definition, hubCommandContext{mode: hubModeSession})
		if available {
			t.Fatalf("/%s available without the SharedNotes capability", name)
		}
		available, _ = hubCommandAvailable(definition, hubCommandContext{
			mode: hubModeSession,
			caps: hubSessionCapabilities{SharedNotes: true},
			live: true,
		})
		if !available {
			t.Fatalf("/%s unavailable with the SharedNotes capability on a live session", name)
		}
	}
}

// TestSharedNotesCommandsRequireLiveness asserts the M7 contract: the
// SharedNotes capability bit is retained on ended sessions for read rendering
// (TestHubDetailFromThreadMapsSharedNotes), so /notes and /url-remove
// additionally require session liveness — for both availability and dispatch
// — and never fire resume-first writes on read-only past sessions.
func TestSharedNotesCommandsRequireLiveness(t *testing.T) {
	for _, name := range []string{"notes", "url-remove"} {
		definition, ok := hubCommandByName(name)
		if !ok {
			t.Fatalf("/%s is not registered", name)
		}
		// Capability set but not live: unavailable.
		available, _ := hubCommandAvailable(definition, hubCommandContext{
			mode: hubModeSession,
			caps: hubSessionCapabilities{SharedNotes: true},
		})
		if available {
			t.Fatalf("/%s available on an ended session with the SharedNotes capability", name)
		}
		// Capability set and live: available.
		available, _ = hubCommandAvailable(definition, hubCommandContext{
			mode: hubModeSession,
			caps: hubSessionCapabilities{SharedNotes: true},
			live: true,
		})
		if !available {
			t.Fatalf("/%s unavailable on a live session with the SharedNotes capability", name)
		}
	}
	// Dispatch on an ended session refuses without issuing a command.
	m := newSessionHubModel(nil)
	m.detail.Capabilities = hubSessionCapabilities{SharedNotes: true}
	m.detail.Live = false
	if cmd := m.runHubNotes("hello"); cmd != nil {
		t.Fatalf("runHubNotes on ended session returned a command, want refusal")
	}
	if cmd := m.runHubURLRemove("u1"); cmd != nil {
		t.Fatalf("runHubURLRemove on ended session returned a command, want refusal")
	}
	// Dispatch on a live session proceeds to a command.
	m.detail.Live = true
	m.detail.Ref = "local:01LIVE"
	if cmd := m.runHubNotes("hello"); cmd == nil {
		t.Fatalf("runHubNotes on live session returned nil, want a send command")
	}
	if cmd := m.runHubURLRemove("u1"); cmd == nil {
		t.Fatalf("runHubURLRemove on live session returned nil, want a send command")
	}
}

// fencedNotesThread builds the wire thread a session under the hub's recovery
// fence presents: live and idle (so Live is true and State is editable by the
// status check) with SharedNotes retained for reading.
func fencedNotesThread(ref string, resumeRequired bool) appwire.Thread {
	return appwire.Thread{
		ID:        "th_1",
		SessionID: "th_1",
		Source:    "local",
		Status:    appwire.ThreadStatus{Type: appwire.ThreadStatusIdle},
		Evener: appwire.EvenerThread{
			Ref:            ref,
			ResumeRequired: resumeRequired,
			Capabilities:   appwire.ThreadCapabilities{SharedNotes: true},
		},
	}
}

// TestSharedNotesCommandUnavailableWhenResumeRequired pins the recovery fence:
// a resumeRequired session presents as a live, idle thread with SharedNotes
// retained for reading, so capability+status alone still advertise mutations
// the hub refuses until an explicit resume. The command, palette, and slash
// help must all refuse, while an unfenced live+idle session keeps offering
// them and saved notes stay readable on the fenced session.
func TestSharedNotesCommandUnavailableWhenResumeRequired(t *testing.T) {
	fenced := hubDetailFromThread(fencedNotesThread("local:01FENCED", true))
	if !fenced.Live || fenced.State != appwire.ThreadStatusIdle {
		t.Fatalf("fenced session should present live+idle: %+v", fenced)
	}
	ctx := hubCommandContext{mode: hubModeSession, caps: fenced.Capabilities, live: fenced.Live, state: fenced.State}

	notes, ok := hubCommandByName("notes")
	if !ok {
		t.Fatal("/notes command not registered")
	}
	urlRemove, ok := hubCommandByName("url-remove")
	if !ok {
		t.Fatal("/url-remove command not registered")
	}
	if available, reason := hubCommandAvailable(notes, ctx); available {
		t.Fatalf("recovery-fenced session advertised /notes: %s", reason)
	}
	if available, reason := hubCommandAvailable(urlRemove, ctx); available {
		t.Fatalf("recovery-fenced session advertised /url-remove: %s", reason)
	}
	// The fence closes writes only; saved notes must stay readable.
	if !fenced.Capabilities.SharedNotes {
		t.Fatalf("recovery fence zeroed SharedNotes; saved notes must stay readable: %+v", fenced)
	}
	for _, entry := range commandPaletteEntriesForSession(hubModeSession, fenced.Capabilities, fenced.Live, fenced.State, nil) {
		if (entry.Command == "notes" || entry.Command == "url-remove") && entry.Item.DisabledReason == "" {
			t.Fatalf("recovery-fenced palette offered /%s", entry.Command)
		}
	}
	if help := hubSlashCommandHelpLive(fenced.Capabilities, fenced.Live, fenced.State); strings.Contains(help, "/notes") || strings.Contains(help, "/url-remove") {
		t.Fatalf("recovery-fenced help offered a mutation:\n%s", help)
	}

	// Negative control: the same session without the fence still advertises
	// both, so the fence is the only difference.
	open := hubDetailFromThread(fencedNotesThread("local:01OPEN", false))
	if available, reason := hubCommandAvailable(notes, hubCommandContext{mode: hubModeSession, caps: open.Capabilities, live: open.Live, state: open.State}); !available {
		t.Fatalf("unfenced live idle session did not advertise /notes: %s", reason)
	}
}

// TestHubNotesDispatchRefusesUnderRecoveryFence pins the Run-handler guards,
// not just command availability: even if a caller reaches the handler, a
// fenced session must not issue a write.
func TestHubNotesDispatchRefusesUnderRecoveryFence(t *testing.T) {
	m := newSessionHubModel(nil)
	m.mode = hubModeSession
	m.detail = hubDetailFromThread(fencedNotesThread("local:01FENCED", true))
	if cmd := m.runHubNotes("hello"); cmd != nil {
		t.Fatal("runHubNotes under the recovery fence returned a command, want refusal")
	}
	if cmd := m.runHubURLRemove("u1"); cmd != nil {
		t.Fatal("runHubURLRemove under the recovery fence returned a command, want refusal")
	}
}
