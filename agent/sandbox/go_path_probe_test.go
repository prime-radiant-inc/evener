package sandbox

import (
	"errors"
	"io/fs"
	"path/filepath"
	"testing"
)

// goPathProbeSystem answers the calls probeGoPath makes: an environment, a home
// directory, the user config directory and the files under it.
type goPathProbeSystem struct {
	stubProbeSystem
	home      string
	configDir string
	files     map[string]string
}

func (s goPathProbeSystem) userHomeDir() (string, error) {
	if s.home == "" {
		return "", errors.New("no home")
	}
	return s.home, nil
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

// A GOPATH set only with `go env -w` lives in the user's go env file, not the
// environment, and the sandbox must find it the way the go command does:
// $GOPATH, then the env file ($GOENV, or <user config dir>/go/env; GOENV=off
// disables it), then $HOME/go.
func TestProbeGoPathFollowsTheGoCommandsPrecedence(t *testing.T) {
	t.Parallel()
	const home, config = "/home/u", "/home/u/.config"
	defaultFile := filepath.Join(config, "go", "env")
	for _, tc := range []struct {
		name  string
		env   map[string]string
		home  string
		files map[string]string
		want  string
	}{
		{"environment wins", map[string]string{"GOPATH": "/from/env"}, home, map[string]string{defaultFile: "GOPATH=/from/file\n"}, "/from/env"},
		{"go env -w file", nil, home, map[string]string{defaultFile: "GOPROXY=direct\nGOPATH=/from/file\n"}, "/from/file"},
		{"later line wins", nil, home, map[string]string{defaultFile: "GOPATH=/first\nGOPATH=/second"}, "/second"},
		{"GOENV names the file", map[string]string{"GOENV": "/elsewhere/env"}, home, map[string]string{"/elsewhere/env": "GOPATH=/from/goenv\n", defaultFile: "GOPATH=/from/file\n"}, "/from/goenv"},
		{"GOENV=off", map[string]string{"GOENV": "off"}, home, map[string]string{defaultFile: "GOPATH=/from/file\n"}, filepath.Join(home, "go")},
		{"no file", nil, home, nil, filepath.Join(home, "go")},
		{"nothing to go on", nil, "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			system := goPathProbeSystem{env: tc.env, home: tc.home, configDir: config, files: tc.files}
			if got := probeGoPath(system); got != tc.want {
				t.Errorf("probeGoPath = %q, want %q", got, tc.want)
			}
		})
	}
}
