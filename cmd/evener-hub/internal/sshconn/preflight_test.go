package sshconn

import (
	"errors"
	"path/filepath"
	"testing"
)

// TestOSArchMappingParity pins the uname mapping to install.sh:21-45. If
// install.sh grows a target, this table and mapOS/mapArch must change together.
func TestOSArchMappingParity(t *testing.T) {
	osCases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"Linux\n", "linux", true},
		{"Darwin\n", "darwin", true},
		{"  Linux  ", "linux", true},
		{"FreeBSD\n", "", false},
		{"Windows_NT\n", "", false},
	}
	for _, tc := range osCases {
		got, ok := mapOS(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("mapOS(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}

	archCases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"x86_64\n", "amd64", true},
		{"amd64\n", "amd64", true},
		{"arm64\n", "arm64", true},
		{"aarch64\n", "arm64", true},
		{"i686\n", "", false},
		{"riscv64\n", "", false},
	}
	for _, tc := range archCases {
		got, ok := mapArch(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("mapArch(%q) = (%q,%v), want (%q,%v)", tc.in, got, ok, tc.want, tc.ok)
		}
	}

	targets := []struct {
		goos, goarch string
		want         bool
	}{
		{"linux", "amd64", true},
		{"darwin", "arm64", true},
		{"linux", "arm64", false},
		{"darwin", "amd64", false},
		{"windows", "amd64", false},
	}
	for _, tc := range targets {
		if got := targetSupported(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("targetSupported(%s/%s) = %v, want %v", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

func TestParseEnvProbe(t *testing.T) {
	env, err := parseEnvProbe([]byte("HOME=/home/dev\nXDG_STATE_HOME=\nXDG_CONFIG_HOME=/xdg/config\n"))
	if err != nil {
		t.Fatalf("parseEnvProbe: %v", err)
	}
	if env["HOME"] != "/home/dev" || env["XDG_STATE_HOME"] != "" || env["XDG_CONFIG_HOME"] != "/xdg/config" {
		t.Fatalf("env = %v", env)
	}

	if _, err := parseEnvProbe([]byte("XDG_STATE_HOME=/x\n")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("missing HOME err = %v, want ErrPreflightDecode", err)
	}
	if _, err := parseEnvProbe([]byte("HOME /home/dev\n")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("malformed line err = %v, want ErrPreflightDecode", err)
	}
}

func TestResolveRootsNoXDGFallsBack(t *testing.T) {
	env := map[string]string{"HOME": "/home/dev", "XDG_STATE_HOME": "", "XDG_CONFIG_HOME": ""}
	configRoot, stateRoot := resolveRoots("linux", env)
	if want := "/home/dev/.config/evener"; configRoot != want {
		t.Errorf("configRoot = %q, want %q", configRoot, want)
	}
	if want := "/home/dev/.local/state/evener"; stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestResolveRootsRespectsXDG(t *testing.T) {
	env := map[string]string{"HOME": "/home/dev", "XDG_STATE_HOME": "/xdg/state", "XDG_CONFIG_HOME": "/xdg/config"}
	configRoot, stateRoot := resolveRoots("linux", env)
	if want := "/xdg/config/evener"; configRoot != want {
		t.Errorf("configRoot = %q, want %q", configRoot, want)
	}
	if want := "/xdg/state/evener"; stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestResolveRootsEmptyHome(t *testing.T) {
	env := map[string]string{"HOME": "", "XDG_STATE_HOME": "", "XDG_CONFIG_HOME": ""}
	configRoot, stateRoot := resolveRoots("linux", env)
	if configRoot != "" {
		t.Errorf("configRoot = %q, want empty (no HOME, no XDG_CONFIG_HOME)", configRoot)
	}
	if want := filepath.Join(".", ".local", "state", "evener"); stateRoot != want {
		t.Errorf("stateRoot = %q, want %q", stateRoot, want)
	}
}

func TestParseLaunchCheck(t *testing.T) {
	lc, err := parseLaunchCheck([]byte(goodLaunchCheck))
	if err != nil {
		t.Fatalf("parseLaunchCheck: %v", err)
	}
	if lc.Protocol != "evener-appwire-v5" || lc.Version != "dev" || len(lc.LaunchFlags) != 1 || lc.LaunchFlags[0] != "api-log" {
		t.Fatalf("launchCheck = %+v", lc)
	}

	if _, err := parseLaunchCheck([]byte("not json")); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("bad json err = %v, want ErrPreflightDecode", err)
	}
	if _, err := parseLaunchCheck([]byte(`{"version":"dev"}`)); !errors.Is(err, ErrPreflightDecode) {
		t.Fatalf("missing protocol err = %v, want ErrPreflightDecode", err)
	}
}

func TestIsProtocolMismatchOutput(t *testing.T) {
	yes := []string{
		`unsupported appwire protocol "evener-appwire-v4" (supported "evener-appwire-v5")`,
		`evener launch-check protocol "x" does not match Hub protocol "y"`,
	}
	for _, s := range yes {
		if !isProtocolMismatchOutput([]byte(s)) {
			t.Errorf("isProtocolMismatchOutput(%q) = false, want true", s)
		}
	}
	if isProtocolMismatchOutput([]byte("connection refused")) {
		t.Error("unrelated output classified as protocol mismatch")
	}
}

func TestIsAuthFailure(t *testing.T) {
	auth := []string{
		"bob@host: Permission denied (publickey).",
		"Host key verification failed.",
		"Authentication failed.",
		"no mutual signature algorithm",
	}
	for _, s := range auth {
		if !isAuthFailure(s) {
			t.Errorf("isAuthFailure(%q) = false, want true", s)
		}
	}
	if isAuthFailure("Connection timed out") {
		t.Error("timeout classified as auth failure")
	}
}
