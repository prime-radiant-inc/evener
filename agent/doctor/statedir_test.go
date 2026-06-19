package doctor

import "testing"

func TestResolveStateBase_Precedence(t *testing.T) {
	t.Setenv("SERF_STATE_DIR", "/env/serfstate")
	t.Setenv("XDG_STATE_HOME", "/env/xdg")

	if got := ResolveStateBase("/flag/dir"); got != "/flag/dir" {
		t.Errorf("flag should win: got %q", got)
	}
	if got := ResolveStateBase(""); got != "/env/serfstate" {
		t.Errorf("SERF_STATE_DIR should win over XDG: got %q", got)
	}

	t.Setenv("SERF_STATE_DIR", "")
	if got := ResolveStateBase(""); got != "/env/xdg" {
		t.Errorf("XDG_STATE_HOME should be used when SERF_STATE_DIR empty: got %q", got)
	}
}
