package userdirs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigRoot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		xdg  string
		home string
		err  error
		want string
	}{
		{name: "xdg wins", xdg: "/xdg", home: "/home", want: "/xdg/evener"},
		{name: "home fallback", home: "/home", want: "/home/.config/evener"},
		{name: "empty home fallback", want: ".config/evener"},
		{name: "home lookup failure", err: os.ErrNotExist, want: ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ConfigRoot(tc.xdg, func() (string, error) { return tc.home, tc.err })
			if got != tc.want {
				t.Fatalf("ConfigRoot() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfigRootPropagatesOnlyHomeAvailability(t *testing.T) {
	t.Parallel()

	called := false
	got := ConfigRoot("/xdg", func() (string, error) {
		called = true
		return "", errors.New("should not be called")
	})
	if got != "/xdg/evener" || called {
		t.Fatalf("ConfigRoot with XDG = %q, home called=%v", got, called)
	}
}

func TestSubdir(t *testing.T) {
	t.Parallel()

	if got := Subdir("/cfg/evener", "skills"); got != "/cfg/evener/skills" {
		t.Fatalf("Subdir() = %q, want %q", got, "/cfg/evener/skills")
	}
	if got := Subdir("", "skills"); got != "" {
		t.Fatalf("Subdir(empty root) = %q, want empty", got)
	}
}

func TestDefaultConfigRootUsesXDGConfigHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)

	if got, want := DefaultConfigRoot(), filepath.Join(root, "evener"); got != want {
		t.Fatalf("DefaultConfigRoot() = %q, want %q", got, want)
	}
}

func TestStateHomeForBucketDir(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		want string
	}{
		{name: "project bucket dir", dir: "/xdg/evener/projects/home-jesse-fSbf9SeZqM", want: "/xdg"},
		{name: "state home itself", dir: "/xdg", want: ""},
		{name: "projects dir, not a bucket", dir: "/xdg/evener/projects", want: ""},
		{name: "override root under projects-named parent", dir: "/tmp/projects/root", want: ""},
		{name: "flat scratch root", dir: "/tmp/scratch", want: ""},
		{name: "trailing separator", dir: "/xdg/evener/projects/home-jesse-fSbf9SeZqM/", want: "/xdg"},
		{name: "doubled separators", dir: "/xdg/evener//projects//b", want: "/xdg"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StateHomeForBucketDir(tc.dir); got != tc.want {
				t.Fatalf("StateHomeForBucketDir(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
}
