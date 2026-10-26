package doctor

import (
	"errors"
	"testing"

	"primeradiant.com/evener/envvars/userdirs"
)

func TestResolveStateBase_Precedence(t *testing.T) {
	t.Setenv("EVENER_STATE_DIR", "/env/evenerstate")
	t.Setenv("XDG_STATE_HOME", "/env/xdg")

	if got, err := ResolveStateBase("/flag/dir"); err != nil || got != "/flag/dir" {
		t.Errorf("flag should win: got %q, %v", got, err)
	}
	if got, err := ResolveStateBase(""); err != nil || got != "/env/evenerstate" {
		t.Errorf("EVENER_STATE_DIR should win over XDG: got %q, %v", got, err)
	}

	t.Setenv("EVENER_STATE_DIR", "")
	if got, err := ResolveStateBase(""); err != nil || got != "/env/xdg" {
		t.Errorf("XDG_STATE_HOME should be used when EVENER_STATE_DIR empty: got %q, %v", got, err)
	}
}

func TestResolveStateBase_NoStateHomeIsAnError(t *testing.T) {
	t.Setenv("EVENER_STATE_DIR", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "")

	if got, err := ResolveStateBase(""); !errors.Is(err, userdirs.ErrNoStateHome) || got != "" {
		t.Errorf("got %q, %v, want ErrNoStateHome", got, err)
	}
	if got, err := ResolveStateBase("/flag/dir"); err != nil || got != "/flag/dir" {
		t.Errorf("an explicit --state-dir needs no home: got %q, %v", got, err)
	}
}
