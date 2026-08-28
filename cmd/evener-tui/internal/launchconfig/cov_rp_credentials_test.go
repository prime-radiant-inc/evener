package launchconfig

import "testing"

func TestFormActiveField(t *testing.T) {
	// Create mode indices.
	create := map[int]string{0: "base", 1: "name", 2: "protocol", 3: "baseURL", 99: "baseURL"}
	for idx, want := range create {
		p := CredentialsPanel{formEditing: false, formField: idx}
		if got := p.formActiveField(); got != want {
			t.Errorf("create formField %d = %q, want %q", idx, got, want)
		}
	}
	// Edit mode indices.
	edit := map[int]string{0: "protocol", 1: "baseURL", 5: "baseURL"}
	for idx, want := range edit {
		p := CredentialsPanel{formEditing: true, formField: idx}
		if got := p.formActiveField(); got != want {
			t.Errorf("edit formField %d = %q, want %q", idx, got, want)
		}
	}
}

func TestFormAppendAndDeleteChar(t *testing.T) {
	// Append to the active field, then delete a char.
	p := &CredentialsPanel{formField: 0} // create mode, field 0 = base
	p.formAppendChar("op")
	p.formAppendChar("enai")
	if p.formBase != "openai" {
		t.Fatalf("formBase = %q, want openai", p.formBase)
	}
	p.formDeleteChar()
	if p.formBase != "opena" {
		t.Fatalf("after delete formBase = %q, want opena", p.formBase)
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

func TestProtocolDisplay(t *testing.T) {
	if got := (CredentialsPanel{}).protocolDisplay(); got != "(default)" {
		t.Fatalf("empty protocolDisplay = %q, want (default)", got)
	}
	if got := (CredentialsPanel{formProtocol: "openai-responses"}).protocolDisplay(); got != "openai-responses" {
		t.Fatalf("protocolDisplay = %q, want openai-responses", got)
	}
}

func TestSourceBadgeColor(t *testing.T) {
	withTestColorProfile(t)
	p := CredentialsPanel{}
	// Every registry source that names a resolved credential shares one tone;
	// "none" (and an unset source) is the one that does not.
	oauth := p.sourceBadgeColor("oauth")
	env := p.sourceBadgeColor("env:GROQ_API_KEY")
	store := p.sourceBadgeColor("store")
	none := p.sourceBadgeColor("none")
	unset := p.sourceBadgeColor("")
	if oauth != env || oauth != store {
		t.Errorf("resolved sources should share a color, got %v, %v, %v", oauth, env, store)
	}
	if none == oauth || unset != none {
		t.Errorf("none/unset should differ from a resolved source: %v vs %v (unset %v)", none, oauth, unset)
	}
}
