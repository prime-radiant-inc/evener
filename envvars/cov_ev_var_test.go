package envvars

import "testing"

// TestVarEnvAccessors exercises the os-backed accessors on a Var: reading a set
// value (raw and trimmed), the LookupEnv presence flag, and Setenv/Unsetenv.
func TestVarEnvAccessors(t *testing.T) {
	v := Var{Name: "SERF_TEST_ACCESSOR"}

	if err := v.Setenv("  spaced  "); err != nil {
		t.Fatal(err)
	}
	if got := v.Getenv(); got != "  spaced  " {
		t.Errorf("Getenv() = %q, want %q", got, "  spaced  ")
	}
	if got := v.Trimmed(); got != "spaced" {
		t.Errorf("Trimmed() = %q, want %q", got, "spaced")
	}
	if got, ok := v.LookupEnv(); !ok || got != "  spaced  " {
		t.Errorf("LookupEnv() = %q, %v; want %q, true", got, ok, "  spaced  ")
	}

	if err := v.Unsetenv(); err != nil {
		t.Fatal(err)
	}
	if got, ok := v.LookupEnv(); ok || got != "" {
		t.Errorf("LookupEnv() after Unsetenv = %q, %v; want \"\", false", got, ok)
	}
	if got := v.Trimmed(); got != "" {
		t.Errorf("Trimmed() after Unsetenv = %q, want \"\"", got)
	}
}

// TestVarFrom covers From/FromTrimmed with an injected getenv and with a nil
// getenv (which falls back to os.Getenv).
func TestVarFrom(t *testing.T) {
	v := Var{Name: "SERF_TEST_FROM"}

	getenv := func(name string) string {
		if name == v.Name {
			return "  value  "
		}
		return ""
	}
	if got := v.From(getenv); got != "  value  " {
		t.Errorf("From(getenv) = %q, want %q", got, "  value  ")
	}
	if got := v.FromTrimmed(getenv); got != "value" {
		t.Errorf("FromTrimmed(getenv) = %q, want %q", got, "value")
	}

	if err := v.Setenv("osvalue"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Unsetenv() })
	if got := v.From(nil); got != "osvalue" {
		t.Errorf("From(nil) = %q, want %q (os fallback)", got, "osvalue")
	}
}

func TestVarAssignment(t *testing.T) {
	v := Var{Name: "SERF_TEST_ASSIGN"}
	if got := v.Assignment("val=with=equals"); got != "SERF_TEST_ASSIGN=val=with=equals" {
		t.Errorf("Assignment() = %q", got)
	}
	if got := v.Assignment(""); got != "SERF_TEST_ASSIGN=" {
		t.Errorf("Assignment(\"\") = %q", got)
	}
}
