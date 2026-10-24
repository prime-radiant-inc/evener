package sshconn

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hostreg"
)

// Only SSH is replaced. Pathname output comes from real filesystem commands.
func TestPathConsumerBytes(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home ")
	newlineDirectory := filepath.Join(root, "physical") + "\n"
	for _, dir := range []string{home, filepath.Join(home, ".local", "bin"), filepath.Join(root, "physical"), filepath.Join(root, "physical "), newlineDirectory} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{filepath.Join(home, "evener"), filepath.Join(home, ".local", "bin", "evener"), filepath.Join(root, "physical", "config"), filepath.Join(root, "physical ", "config"), filepath.Join(root, "physical ", "config "), filepath.Join(newlineDirectory, "config")} {
		if err := os.WriteFile(file, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	spaced := filepath.Join(root, "physical ", "config ")
	alias := filepath.Join(root, "alias")
	newlineAlias := filepath.Join(root, "newline-alias")
	if err := os.Symlink(spaced, alias); err != nil {
		t.Fatal(err)
	}
	a, err := os.Stat(alias)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.Stat(filepath.Join(root, "physical ", "config"))
	if err != nil || os.SameFile(a, b) {
		t.Fatalf("collision fixture is not a different real file: %v", err)
	}
	// A relative link ending in LF must not be silently redirected to "physical".
	if err := os.Symlink("physical\n", newlineAlias); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", strings.Join(argv[12:], " "))
		cmd.Env = []string{"PATH=" + home + ":/usr/bin:/bin", "HOME=" + home}
		cmd.Dir = root
		cmd.Stdin = in
		return cmd.Output()
	}}
	host := hostreg.Host{Name: "bytes", SSH: "fixture.invalid"}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	for _, tc := range []struct {
		name string
		read func() (string, error)
		want string
	}{
		{"deploy", func() (string, error) { return m.resolveDeployTarget(t.Context(), host, alias) }, spaced},
		{"remote", func() (string, error) { return m.resolveRemotePath(t.Context(), host, alias) }, spaced},
		{"create", func() (string, error) {
			return m.resolveDeployOrCreateTarget(t.Context(), host, filepath.Join(home, "new "))
		}, filepath.Join(home, "new ")},
		{"PATH", func() (string, error) { return m.evenerOnPath(t.Context(), host) }, filepath.Join(home, "evener")},
		{"executable-name", func() (string, error) { return m.resolveExecutableName(t.Context(), host, "evener") }, filepath.Join(home, "evener")},
		{"expected-executable", func() (string, error) { return m.expectedHubExecutable(t.Context(), host) }, filepath.Join(home, "evener")},
		{"installer-default", func() (string, error) { return m.installerDefaultForPreflight(t.Context(), host) }, filepath.Join(home, ".local", "bin", "evener")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.read()
			if err != nil || got != tc.want {
				t.Fatalf("path=%q err=%v want=%q", got, err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name, canonical string
		want            bool
	}{
		{"config-positive", spaced, true}, {"config-trim-collision", strings.TrimSuffix(spaced, " "), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			same, err := m.configPathMatches(t.Context(), host, alias, tc.canonical, "")
			if err != nil || same != tc.want {
				t.Fatalf("same=%t err=%v want=%t", same, err, tc.want)
			}
		})
	}
	for _, p := range []string{filepath.Join(newlineAlias, "config"), filepath.Join(newlineDirectory, "missing")} {
		t.Run("newline-refusal", func(t *testing.T) {
			var err error
			if strings.HasSuffix(p, "missing") {
				_, err = m.resolveDeployOrCreateTarget(t.Context(), host, p)
			} else {
				_, err = m.resolveRemotePath(t.Context(), host, p)
			}
			if err == nil {
				t.Fatalf("accepted unrepresentable pathname %q", p)
			}
		})
	}
	t.Run("installer-home", func(t *testing.T) {
		_, _, target, err := installerDirs(host, Preflight{Home: home})
		if err != nil || target != filepath.Join(home, ".local", "bin", "evener") {
			t.Fatalf("target=%q err=%v", target, err)
		}
	})
	for _, name := range []string{filepath.Join(home, "log  with") + "\ttabs ", filepath.Join(home, "plain")} {
		t.Run("log", func(t *testing.T) {
			if err := os.WriteFile(name, []byte("log"), 0o600); err != nil {
				t.Fatal(err)
			}
			data := fmt.Appendf(nil, "evener 4242 dev 1w REG 1,2 123 456 %s\nevener 4242 dev 2w REG 1,2 123 456 %s\n", name, name)
			got, ok := parseLogPath(data)
			if !ok || got != name {
				t.Fatalf("log=%q ok=%t want=%q", got, ok, name)
			}
		})
	}
}

func TestPathConsumerOutputFraming(t *testing.T) {
	for _, framing := range []struct {
		output, want string
		valid        bool
	}{
		{"/physical \n", "/physical ", true},
		{"/physical\t\r\n", "/physical\t\r", true},
		{"/physical", "", false},
		{"/physical\n/other\n", "", false},
		{"/physical\x00\n", "", false},
	} {
		t.Run(fmt.Sprintf("%q", framing.output), func(t *testing.T) {
			fr := &fakeRunner{runFn: func(context.Context, []string, io.Reader) ([]byte, error) { return []byte(framing.output), nil }}
			host := hostreg.Host{Name: "framing", SSH: "fixture.invalid"}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			for _, tc := range []struct {
				name string
				read func() (string, error)
			}{
				{"deploy", func() (string, error) { return m.resolveDeployTarget(t.Context(), host, "/alias") }},
				{"remote", func() (string, error) { return m.resolveRemotePath(t.Context(), host, "/alias") }},
				{"PATH", func() (string, error) { return m.evenerOnPath(t.Context(), host) }},
				{"executable-name", func() (string, error) { return m.resolveExecutableName(t.Context(), host, "evener") }},
				{"expected", func() (string, error) { return m.expectedHubExecutable(t.Context(), host) }},
				{"installer-default", func() (string, error) { return m.installerDefaultForPreflight(t.Context(), host) }},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := tc.read()
					if framing.valid && (err != nil || got != framing.want) || !framing.valid && err == nil {
						t.Fatalf("path=%q err=%v want=%q valid=%t", got, err, framing.want, framing.valid)
					}
				})
			}
		})
	}
	if got := evenerCommand("/opt/evener "); got != "/opt/evener " {
		t.Fatalf("executable pathname changed to %q", got)
	}
	if installableEvenerBasename("/opt/evener ") {
		t.Fatal("accepted a different executable basename")
	}
}

func TestEnvironmentPathFraming(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		valid            bool
	}{
		{"HOME-space", "HOME", "/physical ", true},
		{"state-space", "XDG_STATE_HOME", "/state ", true},
		{"config-space", "XDG_CONFIG_HOME", "/config ", true},
		{"HOME-LF", "HOME", "/physical\n", false},
		{"state-LF", "XDG_STATE_HOME", "/state\n", false},
		{"config-LF", "XDG_CONFIG_HOME", "/config\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", envProbeScript)
			cmd.Env = []string{"HOME=/home", tc.key + "=" + tc.value}
			out, err := cmd.Output()
			if !tc.valid {
				if err == nil || len(out) != 0 {
					t.Fatalf("ambiguous pathname reached line parser: %q err=%v", out, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			env, err := parseEnvProbe(out)
			if err != nil || env[tc.key] != tc.value {
				t.Fatalf("env=%q err=%v want=%q", env, err, tc.value)
			}
		})
	}
}
