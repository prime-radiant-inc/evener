package sandbox

import (
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"
)

// goPathProbeSystem answers the calls goEnvValue makes: an environment, the
// user config directory and the files under it.
type goPathProbeSystem struct {
	stubProbeSystem
	configDir string
	files     map[string]string
}

func (s goPathProbeSystem) userConfigDir() (string, error) {
	if s.configDir == "" {
		return "", errors.New("no config dir")
	}
	return s.configDir, nil
}

func (s goPathProbeSystem) readFile(path string) ([]byte, error) {
	if data, ok := s.files[path]; ok {
		return []byte(data), nil
	}
	return nil, fs.ErrNotExist
}

// A Go setting made with `go env -w` lives in the user's go env file, not the
// environment, and the sandbox must find it the way the go command does: the
// environment first, then the env file ($GOENV, or <user config dir>/go/env;
// GOENV=off disables it). An unset value stays empty, leaving Go's default to
// goPathEntries.
func TestGoEnvValueFollowsTheGoCommandsPrecedence(t *testing.T) {
	t.Parallel()
	const config = "/home/u/.config"
	defaultFile := filepath.Join(config, "go", "env")
	for _, tc := range []struct {
		name  string
		env   map[string]string
		files map[string]string
		want  string
	}{
		{"environment wins", map[string]string{"GOPATH": "/from/env"}, map[string]string{defaultFile: "GOPATH=/from/file\n"}, "/from/env"},
		{"go env -w file", nil, map[string]string{defaultFile: "GOPROXY=direct\nGOPATH=/from/file\n"}, "/from/file"},
		{"later line wins", nil, map[string]string{defaultFile: "GOPATH=/first\nGOPATH=/second"}, "/second"},
		{"GOENV names the file", map[string]string{"GOENV": "/elsewhere/env"}, map[string]string{"/elsewhere/env": "GOPATH=/from/goenv\n", defaultFile: "GOPATH=/from/file\n"}, "/from/goenv"},
		{"GOENV=off", map[string]string{"GOENV": "off"}, map[string]string{defaultFile: "GOPATH=/from/file\n"}, ""},
		{"unset", nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			// env is promoted from stubProbeSystem; Go 1.27 (go.mod) allows a
			// promoted field in a composite literal, and modernize's embedlit asks
			// for this form.
			system := goPathProbeSystem{env: tc.env, configDir: config, files: tc.files}
			if got := goEnvValue(system, "GOPATH"); got != tc.want {
				t.Errorf("goEnvValue(GOPATH) = %q, want %q", got, tc.want)
			}
		})
	}
}

// goPathEntries holds Go's GOPATH rule in one place: the absolute entries of the
// configured value (the go command refuses relative ones), else $HOME/go.
func TestGoPathEntriesApplyGosDefault(t *testing.T) {
	t.Parallel()
	sep := string(filepath.ListSeparator)
	for _, tc := range []struct {
		name string
		host HostFacts
		want []string
	}{
		{"configured", HostFacts{Home: "/home/u", GoPath: "/a" + sep + "relative" + sep + "/b"}, []string{"/a", "/b"}},
		{"default", HostFacts{Home: "/home/u"}, []string{"/home/u/go"}},
		{"only relative", HostFacts{Home: "/home/u", GoPath: "relative"}, []string{"/home/u/go"}},
		{"no home", HostFacts{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := goPathEntries(tc.host); !slices.Equal(got, tc.want) {
				t.Errorf("goPathEntries = %q, want %q", got, tc.want)
			}
		})
	}
}
