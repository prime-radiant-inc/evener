package gittest

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// quiescentSettings are the git settings every test git runs under. Automatic
// maintenance and gc are off because either can leave a detached process
// writing into a repository after the command that started it has returned,
// racing the test's cleanup of that repository.
var quiescentSettings = []struct{ section, name, value string }{
	{"maintenance", "auto", "false"},
	{"gc", "auto", "0"},
}

// IsolateProcess makes every git this process and its children spawn, the
// harness's and the code under test's alike, read one global config file
// holding the quiescent settings and no system config. It removes the ambient
// config-injection and init-template variables Env strips, so the host's git
// configuration cannot reach a test repository. Call it from TestMain before
// m.Run, and call remove, which deletes the config file, after it.
func IsolateProcess() (remove func(), err error) {
	dir, err := os.MkdirTemp("", "gittest-config-*")
	if err != nil {
		return nil, err
	}
	remove = func() { _ = os.RemoveAll(dir) }
	defer func() {
		if err != nil {
			remove()
			remove = nil
		}
	}()

	var config strings.Builder
	for _, setting := range quiescentSettings {
		fmt.Fprintf(&config, "[%s]\n\t%s = %s\n", setting.section, setting.name, setting.value)
	}
	path := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(path, []byte(config.String()), 0o644); err != nil {
		return nil, err
	}
	for _, entry := range os.Environ() {
		if key, _, _ := strings.Cut(entry, "="); isConfigEnvironment(key) {
			if err := os.Unsetenv(key); err != nil {
				return nil, err
			}
		}
	}
	if err := os.Setenv("GIT_CONFIG_GLOBAL", path); err != nil {
		return nil, err
	}
	return remove, os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// quiescentEnvironment injects the quiescent settings through git's
// GIT_CONFIG_COUNT variables, for a command whose global config is empty.
func quiescentEnvironment() []string {
	env := []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(len(quiescentSettings))}
	for i, setting := range quiescentSettings {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s.%s", i, setting.section, setting.name),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, setting.value),
		)
	}
	return env
}

// RequireIsolated fails t unless git run through run in the repository at dir
// sees the quiescent settings, no system-scope config and no global config
// beyond them. A package passes the
// runner its code under test spawns git through, so the check covers the
// environment that code really builds.
func RequireIsolated(t testing.TB, run Runner, dir string) {
	t.Helper()
	for _, setting := range quiescentSettings {
		key := setting.section + "." + setting.name
		out, err := run(dir, "config", "--get", key)
		if err != nil {
			t.Errorf("git config --get %s: %v", key, err)
			continue
		}
		if got := strings.TrimSpace(out); got != setting.value {
			t.Errorf("git config --get %s = %q, want %q", key, got, setting.value)
		}
	}
	listing, err := run(dir, "config", "--list", "--show-scope")
	if err != nil {
		t.Fatalf("git config --list --show-scope: %v", err)
	}
	quiescent := make(map[string]bool, len(quiescentSettings))
	for _, setting := range quiescentSettings {
		quiescent[setting.section+"."+setting.name+"="+setting.value] = true
	}
	for line := range strings.Lines(listing) {
		scope, entry, _ := strings.Cut(strings.TrimSpace(line), "\t")
		switch {
		case scope == "system":
			t.Errorf("git read system-scope config: %q", entry)
		case scope == "global" && !quiescent[entry]:
			t.Errorf("git read host global config: %q", entry)
		}
	}
}
