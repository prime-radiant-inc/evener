package execenv

import (
	"os/exec"
	"testing"
)

// TestShellEscapeArgsIsPOSIXShellOnly pins the scope the ShellEscapeArgs doc
// states: the rendered line is quoted for a POSIX shell, and Windows callers
// must not use it to build an ExecCommand string, because cmd.exe treats a
// single quote as ordinary text. The quoting itself is deliberately unchanged;
// this test freezes the bytes that scope statement describes and proves the
// POSIX half of the contract against a real sh.
func TestShellEscapeArgsIsPOSIXShellOnly(t *testing.T) {
	// '&' is a POSIX metacharacter, so the whole token is single-quoted and a
	// POSIX shell passes it through as one literal argument. On cmd.exe the same
	// token is not quoted at all — the apostrophes are data and '&' still
	// separates commands (see the doc comment).
	in := "a & calc &"
	if got, want := ShellEscapeArgs(in), "'a & calc &'"; got != want {
		t.Fatalf("ShellEscapeArgs(%q) = %q, want %q", in, got, want)
	}
	if got, want := ShellEscapeArgs("echo", in), "echo 'a & calc &'"; got != want {
		t.Fatalf("ShellEscapeArgs(echo, %q) = %q, want %q", in, got, want)
	}
	// '%' is not a POSIX metacharacter, so it stays bare by allow-list design.
	// cmd.exe would expand this token as %VAR%, which is exactly why a Windows
	// caller must exec from an argument vector (ExecArgv) instead.
	if got, want := ShellEscapeArgs("%VAR%"), "%VAR%"; got != want {
		t.Fatalf("ShellEscapeArgs(%%VAR%%) = %q, want the bare %q", got, want)
	}

	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("no POSIX sh on PATH: %v", err)
	}
	out, err := exec.Command(sh, "-c", "printf '%s' "+ShellEscapeArgs(in)).Output()
	if err != nil {
		t.Fatalf("sh rejected ShellEscapeArgs(%q): %v", in, err)
	}
	if string(out) != in {
		t.Fatalf("ShellEscapeArgs(%q) round-tripped through sh to %q", in, out)
	}
}
