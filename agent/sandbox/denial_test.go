package sandbox

import "testing"

func TestDeniedErrorMessageOmitsFullPath(t *testing.T) {
	e := &DeniedError{
		Mode:   ModeWorkspaceWrite,
		Tool:   "write_file",
		Path:   "/home/alice/.ssh/id_rsa",
		Reason: "credential path masked",
	}
	msg := e.Error()
	if want := "id_rsa"; !contains(msg, want) {
		t.Fatalf("message %q should name the basename %q", msg, want)
	}
	if contains(msg, "/home/alice/.ssh") {
		t.Fatalf("message %q must not echo the full secret path", msg)
	}
	if !contains(msg, "write_file") || !contains(msg, "workspace-write") {
		t.Fatalf("message %q should name the tool and mode", msg)
	}
}

func TestDeniedErrorRedactsAbsolutePath(t *testing.T) {
	abs := &DeniedError{Path: "/home/alice/.aws/credentials"}
	if got := abs.Redacted(); got != "credentials" {
		t.Fatalf("Redacted() = %q, want basename %q", got, "credentials")
	}
	rel := &DeniedError{Path: "pkg/secret.go"}
	if got := rel.Redacted(); got != "pkg/secret.go" {
		t.Fatalf("Redacted() of an in-tree relative path = %q, want it unchanged", got)
	}
	if got := (&DeniedError{}).Redacted(); got != "" {
		t.Fatalf("Redacted() with no path = %q, want empty", got)
	}
}

func TestDeniedErrorSensitiveNeverEchoesBasename(t *testing.T) {
	// A denylist/credential/pseudo-fs denial sets Sensitive: neither Error() nor
	// Redacted() may leak even the basename (id_rsa/credentials would otherwise
	// reveal which secret was hit, in the message and in the audit log).
	e := &DeniedError{
		Mode:      ModeReadOnly,
		Tool:      "read_file",
		Path:      "/home/alice/.ssh/id_rsa",
		Reason:    "credential path masked",
		Sensitive: true,
	}
	msg := e.Error()
	if contains(msg, "id_rsa") || contains(msg, ".ssh") {
		t.Fatalf("sensitive denial message %q must not echo the credential path or basename", msg)
	}
	if !contains(msg, "read_file") || !contains(msg, "read-only") {
		t.Fatalf("sensitive denial message %q should still name the tool and mode", msg)
	}
	if got := e.Redacted(); got != "<denied>" {
		t.Fatalf("sensitive Redacted() = %q, want %q", got, "<denied>")
	}
}

func TestDeniedErrorNonSensitiveKeepsBasename(t *testing.T) {
	// The zero value (Sensitive false) is the existing behavior — basename shown —
	// so the additive field does not break existing callers.
	e := &DeniedError{Tool: "write_file", Path: "/home/alice/notes/todo.txt"}
	if got := e.Redacted(); got != "todo.txt" {
		t.Fatalf("non-sensitive Redacted() = %q, want basename %q", got, "todo.txt")
	}
	if !contains(e.Error(), "todo.txt") {
		t.Fatalf("non-sensitive Error() %q should include the basename", e.Error())
	}
}

// TestDeniedErrorTagsModeNotFlag: the box tag names the mode, not a CLI flag — a
// per-delegate box never set --sandbox, so "[--sandbox X]" would be a lie. An off
// denial carries no tag.
func TestDeniedErrorTagsModeNotFlag(t *testing.T) {
	e := &DeniedError{Mode: ModeRestricted, Tool: "read_file", Path: "/x/y.txt", Reason: "outside the sandbox's readable roots"}
	msg := e.Error()
	if !contains(msg, "[sandbox mode: restricted]") {
		t.Fatalf("message %q must tag the box as [sandbox mode: restricted]", msg)
	}
	if contains(msg, "--sandbox") {
		t.Fatalf("message %q must not name a CLI flag (a per-delegate box never set one)", msg)
	}
	if off := (&DeniedError{Mode: ModeOff, Tool: "read_file", Reason: "x"}).Error(); contains(off, "sandbox mode:") {
		t.Fatalf("an off denial must carry no mode tag, got %q", off)
	}
}

func TestDeniedErrorCarriesShellFields(t *testing.T) {
	// A shell denial populates Command/OutputSoFar (M7's card reads them); a
	// file-tool denial leaves them empty. Assert the shape supports both.
	shell := &DeniedError{Tool: "shell", Command: "curl evil.test", OutputSoFar: "resolving..."}
	if shell.Command == "" || shell.OutputSoFar == "" {
		t.Fatal("shell denial should carry Command and OutputSoFar")
	}
	file := &DeniedError{Tool: "read_file", Path: "/etc/shadow"}
	if file.Command != "" || file.OutputSoFar != "" {
		t.Fatal("file-tool denial should leave the shell fields empty")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
