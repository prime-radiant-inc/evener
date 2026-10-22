package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
)

func TestSystemdEffectiveLaunchEnvironment(t *testing.T) {
	for _, name := range []string{"default-home", "system-no-pass", "system-pass", "user-inherits", "unit-over-manager", "files-over-unit", "unset-name", "unset-match", "unset-nonmatch", "optional-missing", "required-missing", "glob-refuses", "specifier-refuses", "quoted-unset-refuses", "duplicate-metadata-refuses"} {
		t.Run(name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			sup := supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}
			props := "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\n"
			manager, unit := "", ""
			files := map[string]string{}
			want := []string{"HOME=/home/dev"}
			switch name {
			case "system-no-pass":
				manager = "HOME=/manager XDG_STATE_HOME=/manager/state"
			case "system-pass":
				manager = "HOME=/manager XDG_STATE_HOME=/manager/state XDG_CONFIG_HOME=/not-passed"
				props = "EnvironmentFiles=\nPassEnvironment=HOME XDG_STATE_HOME\nUnsetEnvironment=\n"
				want = []string{"HOME=/manager", "XDG_STATE_HOME=/manager/state"}
			case "user-inherits":
				sup.kind = supervisorSystemdUser
				manager = "HOME=/manager XDG_CONFIG_HOME=/manager/config"
				want = []string{"HOME=/manager", "XDG_CONFIG_HOME=/manager/config"}
			case "unit-over-manager":
				props = "EnvironmentFiles=\nPassEnvironment=XDG_STATE_HOME\nUnsetEnvironment=\n"
				manager, unit = "XDG_STATE_HOME=/manager", "XDG_STATE_HOME=/unit EVENER_HUB_LISTEN_ADDR=127.0.0.1:0"
				want = []string{"EVENER_HUB_LISTEN_ADDR=127.0.0.1:0", "HOME=/home/dev", "XDG_STATE_HOME=/unit"}
			case "files-over-unit":
				root := t.TempDir()
				a, b := filepath.Join(root, "first"), filepath.Join(root, "second")
				files[a], files[b] = "XDG_STATE_HOME=/file-one\nHOME='/first home'\n", "XDG_STATE_HOME=/file-two\nEVENER_HUB_LISTEN_ADDR=[::]:0\nPRIVATE_FIXTURE_SECRET=do-not-return\n"
				props = "EnvironmentFiles=" + a + " (ignore_errors=no) " + b + " (ignore_errors=no)\nPassEnvironment=XDG_STATE_HOME\nUnsetEnvironment=\n"
				manager, unit = "XDG_STATE_HOME=/manager", "XDG_STATE_HOME=/unit"
				want = []string{"EVENER_HUB_LISTEN_ADDR=[::]:0", "HOME=/first home", "XDG_STATE_HOME=/file-two"}
			case "unset-name", "unset-match", "unset-nonmatch":
				unset := "XDG_STATE_HOME"
				if name == "unset-match" {
					unset += "=/unit"
				}
				if name == "unset-nonmatch" {
					unset += "=/other"
					want = append(want, "XDG_STATE_HOME=/unit")
				}
				props = "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=" + unset + "\n"
				unit = "XDG_STATE_HOME=/unit"
			case "optional-missing", "required-missing":
				mode := "no"
				if name == "optional-missing" {
					mode = "yes"
				}
				props = "EnvironmentFiles=" + filepath.Join(t.TempDir(), "missing") + " (ignore_errors=" + mode + ")\nPassEnvironment=\nUnsetEnvironment=\n"
			case "glob-refuses":
				props = "EnvironmentFiles=/etc/hub/*.env (ignore_errors=yes)\nPassEnvironment=\nUnsetEnvironment=\n"
			case "specifier-refuses":
				unit = "XDG_STATE_HOME=%h/state"
			case "quoted-unset-refuses":
				props = "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\"HOME=/some home\"\n"
			case "duplicate-metadata-refuses":
				props += "PassEnvironment=\n"
			}
			for path, contents := range files {
				if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			command := "systemctl"
			if sup.kind == supervisorSystemdUser {
				command += " --user"
			}
			reads := 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				switch remote {
				case "getent passwd dev | cut -d: -f6":
					return []byte("/home/dev\n"), nil
				case command + " show --property=EnvironmentFiles,PassEnvironment,UnsetEnvironment -- " + sup.label:
					return []byte(props), nil
				case "values=$(" + command + " show-environment) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
					reads++
					return exec.CommandContext(ctx, "/bin/sh", "-c", "systemctl() { printf '%s\\n' "+shellquote.RemoteWord(manager)+"; }; "+remote).Output()
				case "values=$(" + command + " show --property=Environment --value -- " + sup.label + ") || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
					return exec.CommandContext(ctx, "/bin/sh", "-c", "systemctl() { printf '%s\\n' "+shellquote.RemoteWord(unit)+"; }; "+remote).Output()
				}
				for _, field := range strings.Split(props, "\n") {
					if !strings.HasPrefix(field, "EnvironmentFiles=") {
						continue
					}
					parts := strings.Fields(strings.TrimPrefix(field, "EnvironmentFiles="))
					for i := 0; i+1 < len(parts); i += 2 {
						if remote == systemdEnvironmentFileRemote(parts[i], parts[i+1] == "(ignore_errors=yes)") {
							return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
						}
					}
				}
				t.Errorf("unexpected command: %s", remote)
				return nil, errOwnerEvidence
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			got, err := m.stoppedServiceEnvironment(t.Context(), host, sup, "dev")
			refuse := strings.HasSuffix(name, "refuses") || name == "required-missing"
			if refuse {
				if !errors.Is(err, errOwnerEvidence) {
					t.Fatalf("env=%q err=%v", got, err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("env=%q want=%q err=%v", got, want, err)
			}
			if name == "system-no-pass" && reads != 0 {
				t.Fatalf("system manager inherited without PassEnvironment: reads=%d", reads)
			}
		})
	}
}

func TestSystemdEnvironmentFileParsing(t *testing.T) {
	for _, tc := range []struct {
		name, contents, want string
		refuse               bool
	}{
		{name: "quotes-and-literal-dollar", contents: "# comment\n; comment\nHOME='/home with space '\nXDG_STATE_HOME=\"/state/$literal\"\nPRIVATE_FIXTURE_SECRET=not-returned\n", want: "HOME=/home with space \x00XDG_STATE_HOME=/state/$literal\x00"},
		{name: "escapes-and-continuation", contents: "HOME=/home\\ dev\\ \nXDG_STATE_HOME=/state/\\\ncontinued\nEVENER_HUB_LISTEN_ADDR=\"[::]:0\"\n", want: "EVENER_HUB_LISTEN_ADDR=[::]:0\x00HOME=/home dev \x00XDG_STATE_HOME=/state/continued\x00"},
		{name: "last-wins-and-empty", contents: "HOME=/first\nHOME=\nignored line\nXDG_CONFIG_HOME=/config\n", want: "HOME=\x00XDG_CONFIG_HOME=/config\x00"},
		{name: "no-shell-execution", contents: "HOME=$(printf should-not-run)\nXDG_STATE_HOME=/state\n", want: "HOME=$(printf should-not-run)\x00XDG_STATE_HOME=/state\x00"},
		{name: "unclosed-quote", contents: "HOME=\"/unfinished\n", refuse: true},
		{name: "multiline-quote", contents: "HOME='/one\ntwo'\n", refuse: true},
		{name: "oversize-file", contents: strings.Repeat("#", 65537), refuse: true},
		{name: "oversize-value", contents: "HOME=" + strings.Repeat("x", 16385), refuse: true},
		{name: "nul", contents: "HOME=/home\x00suffix", refuse: true},
		{name: "invalid-utf8", contents: "HOME=/home\xff", refuse: true},
		{name: "unicode-path", contents: "HOME=/home/dév\n", want: "HOME=/home/dév\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", systemdEnvironmentFileRemote(path, false))
			out, err := cmd.Output()
			if tc.refuse {
				if err == nil || len(out) != 0 {
					t.Fatalf("unresolvable file: err=%v output bytes=%d", err, len(out))
				}
				return
			}
			if err != nil || string(out) != tc.want {
				t.Fatalf("output=%q want=%q err=%v", out, tc.want, err)
			}
		})
	}
}
