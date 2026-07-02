package launchconfig

import "testing"

func TestFormActiveField(t *testing.T) {
	// Create mode indices.
	create := map[int]string{0: "type", 1: "name", 2: "apiStyle", 3: "baseURL", 99: "baseURL"}
	for idx, want := range create {
		p := CredentialsPanel{formEditing: false, formField: idx}
		if got := p.formActiveField(); got != want {
			t.Errorf("create formField %d = %q, want %q", idx, got, want)
		}
	}
	// Edit mode indices.
	edit := map[int]string{0: "apiStyle", 1: "baseURL", 5: "baseURL"}
	for idx, want := range edit {
		p := CredentialsPanel{formEditing: true, formField: idx}
		if got := p.formActiveField(); got != want {
			t.Errorf("edit formField %d = %q, want %q", idx, got, want)
		}
	}
}

func TestToggleAPIStyle(t *testing.T) {
	p := &CredentialsPanel{formAPIStyle: "chat-completions"}
	p.toggleAPIStyle()
	if p.formAPIStyle != "responses" {
		t.Fatalf("after toggle = %q, want responses", p.formAPIStyle)
	}
	p.toggleAPIStyle()
	if p.formAPIStyle != "chat-completions" {
		t.Fatalf("after second toggle = %q, want chat-completions", p.formAPIStyle)
	}
	// Any non-"responses" value (including empty) toggles to chat-completions.
	empty := &CredentialsPanel{}
	empty.toggleAPIStyle()
	if empty.formAPIStyle != "chat-completions" {
		t.Fatalf("empty toggle = %q, want chat-completions", empty.formAPIStyle)
	}
}

func TestFormAppendAndDeleteChar(t *testing.T) {
	// Append to the active field, then delete a char.
	p := &CredentialsPanel{formField: 0} // create mode, field 0 = type
	p.formAppendChar("op")
	p.formAppendChar("enai")
	if p.formType != "openai" {
		t.Fatalf("formType = %q, want openai", p.formType)
	}
	p.formDeleteChar()
	if p.formType != "opena" {
		t.Fatalf("after delete formType = %q, want opena", p.formType)
	}

	// baseURL field in edit mode.
	e := &CredentialsPanel{formEditing: true, formField: 1}
	e.formAppendChar("https://x")
	if e.formBaseURL != "https://x" {
		t.Fatalf("formBaseURL = %q", e.formBaseURL)
	}
	e.formDeleteChar()
	if e.formBaseURL != "https://" {
		t.Fatalf("after delete formBaseURL = %q", e.formBaseURL)
	}

	// Deleting from an empty field is a no-op (no panic, stays empty).
	n := &CredentialsPanel{formField: 1} // create mode, field 1 = name
	n.formDeleteChar()
	if n.formName != "" {
		t.Fatalf("empty delete formName = %q, want empty", n.formName)
	}
	n.formAppendChar("hi")
	if n.formName != "hi" {
		t.Fatalf("formName = %q", n.formName)
	}
}

func TestApiStyleDisplay(t *testing.T) {
	if got := (CredentialsPanel{}).apiStyleDisplay(); got != "(default)" {
		t.Fatalf("empty apiStyleDisplay = %q, want (default)", got)
	}
	if got := (CredentialsPanel{formAPIStyle: "responses"}).apiStyleDisplay(); got != "responses" {
		t.Fatalf("apiStyleDisplay = %q, want responses", got)
	}
}

func TestSourceBadgeColor(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{}
	// Distinct sources map to distinct theme colors; exercise every branch.
	oauth := p.sourceBadgeColor("oauth")
	env := p.sourceBadgeColor("env")
	absent := p.sourceBadgeColor("absent")
	other := p.sourceBadgeColor("something-else")
	if oauth != env {
		t.Errorf("oauth and env should share a color, got %v vs %v", oauth, env)
	}
	if absent == oauth || other == oauth {
		t.Errorf("absent/other should differ from oauth color")
	}
}
