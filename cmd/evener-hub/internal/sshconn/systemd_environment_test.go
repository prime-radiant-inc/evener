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
	for _, name := range []string{"default-home", "system-no-pass", "system-pass", "user-inherits", "unit-over-manager", "files-over-unit", "unset-name", "unset-match", "unset-nonmatch", "optional-missing", "required-missing", "glob-refuses", "specifier-refuses", "quoted-unset-refuses", "duplicate-metadata-refuses", "no-login-home-absent", "no-login-home-empty", "no-login-home-manager", "no-login-home-unit", "no-login-home-file", "no-login-user-inherits", "no-login-home-unset", "login-policy-invalid-refuses", "legacy-login-home", "legacy-unit-home", "legacy-home-unset", "legacy-home-empty", "legacy-home-unit", "legacy-empty-user", "legacy-user-home-default", "empty-user-home-unit", "empty-user-home-empty", "empty-user-home-file", "empty-user-home-manager", "empty-user-home-not-passed", "metadata-missing-user-refuses", "login-policy-empty-refuses", "unit-home-name", "unit-home-uid", "unit-home-root", "unit-home-default", "user-home-default", "unit-home-explicit-empty"} {
		t.Run(name, func(t *testing.T) {
			host, _ := ownerCommands(t)
			sup := supervisor{kind: supervisorSystemd, label: "evener-hub-selected.service"}
			props := "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\n"
			policy := "yes"
			omitPolicy := strings.HasPrefix(name, "legacy-")
			unitUser := "dev"
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
			case "no-login-home-absent", "no-login-home-empty", "no-login-home-manager", "no-login-home-unit", "no-login-home-file", "no-login-user-inherits", "no-login-home-unset":
				policy = "no"
				unit = "XDG_CONFIG_HOME=/config XDG_STATE_HOME=/state"
				want = []string{"XDG_CONFIG_HOME=/config", "XDG_STATE_HOME=/state"}
				switch name {
				case "no-login-home-absent":
					manager = "HOME=/not-passed"
				case "no-login-home-empty":
					unit += " HOME="
					want = append([]string{"HOME="}, want...)
				case "no-login-home-manager":
					props = "EnvironmentFiles=\nPassEnvironment=HOME\nUnsetEnvironment=\n"
					manager = "HOME=/manager"
					want = append([]string{"HOME=/manager"}, want...)
				case "no-login-home-unit":
					unit += " HOME=/unit"
					want = append([]string{"HOME=/unit"}, want...)
				case "no-login-home-file":
					path := filepath.Join(t.TempDir(), "home.env")
					files[path] = "HOME=/file\n"
					props = "EnvironmentFiles=" + path + " (ignore_errors=no)\nPassEnvironment=HOME\nUnsetEnvironment=\n"
					manager, unit = "HOME=/manager", unit+" HOME=/unit"
					want = append([]string{"HOME=/file"}, want...)
				case "no-login-user-inherits":
					sup.kind, manager = supervisorSystemdUser, "HOME=/manager"
					want = append([]string{"HOME=/manager"}, want...)
				case "no-login-home-unset":
					unit += " HOME=/unit"
					props = "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=HOME\n"
				}
			case "login-policy-invalid-refuses":
				policy = "unknown"
			case "login-policy-empty-refuses":
				policy = ""
			case "legacy-unit-home":
				unitUser, want = "hubsvc", []string{"HOME=/home/hubsvc"}
			case "legacy-home-unset":
				props = "EnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=HOME\n"
				want = nil
			case "legacy-home-empty":
				unit, want = "HOME=", []string{"HOME="}
			case "legacy-home-unit":
				unit, want = "HOME=/unit", []string{"HOME=/unit"}
			case "unit-home-name":
				unitUser, want = "hubsvc", []string{"HOME=/home/hubsvc"}
			case "unit-home-uid":
				unitUser, want = "1000", []string{"HOME=/home/uid-1000"}
			case "unit-home-root":
				unitUser, want = "root", []string{"HOME=/root"}
			case "unit-home-default", "legacy-empty-user":
				unitUser, want = "", nil
			case "user-home-default", "legacy-user-home-default":
				sup.kind, unitUser = supervisorSystemdUser, ""
			case "empty-user-home-unit":
				unitUser, unit, want = "", "HOME=/unit", []string{"HOME=/unit"}
			case "empty-user-home-empty":
				unitUser, unit, want = "", "HOME=", []string{"HOME="}
			case "empty-user-home-file":
				unitUser = ""
				path := filepath.Join(t.TempDir(), "home.env")
				files[path], unit, want = "HOME=/file\n", "HOME=/unit", []string{"HOME=/file"}
				props = "EnvironmentFiles=" + path + " (ignore_errors=no)\nPassEnvironment=\nUnsetEnvironment=\n"
			case "empty-user-home-manager":
				unitUser, manager, want = "", "HOME=/manager", []string{"HOME=/manager"}
				props = "EnvironmentFiles=\nPassEnvironment=HOME\nUnsetEnvironment=\n"
			case "empty-user-home-not-passed":
				unitUser, manager, want = "", "HOME=/not-passed", nil
			case "unit-home-explicit-empty":
				unitUser, unit, want = "hubsvc", "HOME=", []string{"HOME="}
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
			reads, passwdReads := 0, 0
			fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, in io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if strings.HasPrefix(remote, "getent passwd ") {
					passwdReads++
				}
				switch remote {
				case "getent passwd dev | cut -d: -f6":
					return []byte("/home/dev\n"), nil
				case "getent passwd hubsvc | cut -d: -f6":
					return []byte("/home/hubsvc\n"), nil
				case "getent passwd 1000 | cut -d: -f6":
					return []byte("/home/uid-1000\n"), nil
				case "getent passwd root | cut -d: -f6":
					return []byte("/root\n"), nil
				case command + " show --property=User,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + sup.label:
					result := "User=" + unitUser + "\n" + props
					if name == "metadata-missing-user-refuses" {
						result = props
					}
					if !omitPolicy {
						result += "SetLoginEnvironment=" + policy + "\n"
					}
					return []byte(result), nil
				case "values=$(" + command + " show-environment) || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
					reads++
					return exec.CommandContext(ctx, "/bin/sh", "-c", "systemctl() { printf '%s\\n' "+shellquote.RemoteWord(manager)+"; }; "+remote).Output()
				case "values=$(" + command + " show --property=Environment --value -- " + sup.label + ") || exit; printf '%s\\n' \"$values\" | " + narrowEnvironmentFilter:
					return exec.CommandContext(ctx, "/bin/sh", "-c", "systemctl() { printf '%s\\n' "+shellquote.RemoteWord(unit)+"; }; "+remote).Output()
				}
				for field := range strings.SplitSeq(props, "\n") {
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
			if sup.kind == supervisorSystemd && unitUser == "" && passwdReads != 0 {
				t.Fatalf("empty system User queried passwd: reads=%d", passwdReads)
			}
			if name == "system-no-pass" && reads != 0 {
				t.Fatalf("system manager inherited without PassEnvironment: reads=%d", reads)
			}
		})
	}
}

func TestSystemdLoginEnvironmentDefinition(t *testing.T) {
	for _, kind := range []supervisorKind{supervisorSystemd, supervisorSystemdUser} {
		t.Run(string(kind), func(t *testing.T) {
			for _, initial := range []string{"no", ""} {
				t.Run("policy="+initial, func(t *testing.T) {
					host, _ := ownerCommands(t)
					sup := supervisor{kind: kind, label: "evener-hub-selected.service"}
					policy := initial
					show := "show --property=User,ExecStart,WorkingDirectory,Environment,EnvironmentFiles,PassEnvironment,UnsetEnvironment,SetLoginEnvironment -- " + sup.label
					if kind == supervisorSystemdUser {
						show = "--user " + show
					}
					base := "User=dev\nExecStart=/opt/evener hub\nWorkingDirectory=/srv\nEnvironment=\nEnvironmentFiles=\nPassEnvironment=\nUnsetEnvironment=\n"
					fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
						properties := base
						if policy != "" {
							properties += "SetLoginEnvironment=" + policy + "\n"
						}
						stub := "systemctl() { case \"$*\" in " + shellquote.RemoteWord(show) + ") printf '%s' " + shellquote.RemoteWord(properties) + ";; show-environment|'--user show-environment') :;; *) return 1;; esac; }; "
						return exec.CommandContext(ctx, "/bin/sh", "-c", stub+strings.Join(argv[12:], " ")).Output()
					}}
					m := newTestManager(t, testRegistry(t, host), fr, Options{})
					before, err := m.serviceDefinition(t.Context(), host, sup)
					if err != nil {
						t.Fatal(err)
					}
					repeated, err := m.serviceDefinition(t.Context(), host, sup)
					if err != nil || before != repeated {
						t.Fatalf("unchanged login policy invalidated definition: before=%q repeated=%q err=%v", before, repeated, err)
					}
					policy = "yes"
					after, err := m.serviceDefinition(t.Context(), host, sup)
					if err != nil || before == after {
						t.Fatalf("login policy change did not invalidate definition: before=%q after=%q err=%v", before, after, err)
					}
				})
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
		{name: "double-quote-escapes", contents: "HOME=\"/home/\\$d/\\`b/\\\"q/\\\\s/\\opaque\"\n", want: "HOME=/home/$d/`b/\"q/\\s/\\opaque\x00"},
		{name: "single-quote-literals", contents: "HOME='/home/\\$`\"literal'\n", want: "HOME=/home/\\$`\"literal\x00"},
		{name: "double-quote-continuation", contents: "HOME=\"/home/\\\ncontinued\"\n", want: "HOME=/home/continued\x00"},
		{name: "literal-interior-quotes", contents: "HOME=/home/'some'\"thing\"\n", want: "HOME=/home/'some'\"thing\"\x00"},
		{name: "spacing", contents: " \t# ignored\n\t; ignored\n \tHOME \t= \t/home/dev \t\r\n", want: "HOME=/home/dev\x00"},
		{name: "invalid-key-ignored", contents: "1BAD=ignored\nHOME=/home\n", want: "HOME=/home\x00"},
		{name: "empty-quoted", contents: "HOME=''\nXDG_STATE_HOME=\"\"\n", want: "HOME=\x00XDG_STATE_HOME=\x00"},
		{name: "double-backslash", contents: "HOME=/home/\\\\\n", want: "HOME=/home/\\\x00"},
		{name: "max-file", contents: strings.Repeat("#", 65536)},
		{name: "bounded-fetch", contents: strings.Repeat("#", 131072), refuse: true},
		{name: "max-value", contents: "HOME=" + strings.Repeat("x", 16384), want: "HOME=" + strings.Repeat("x", 16384) + "\x00"},
		{name: "single-quote-suffix", contents: "HOME='/home'garbage\n", refuse: true},
		{name: "double-quote-suffix", contents: "HOME=\"/home\"garbage\n", refuse: true},
		{name: "double-multiline-quote", contents: "HOME=\"/one\ntwo\"\n", refuse: true},
		{name: "single-quoted-backslash-newline", contents: "HOME='/one\\\ntwo'\n", refuse: true},
		{name: "dangling-continuation", contents: "HOME=/home\\", refuse: true},
		{name: "carriage-return-value", contents: "HOME='/home\rdev'\n", refuse: true},
		{name: "bom", contents: "\ufeffHOME=/home\n", refuse: true},
		{name: "noncharacter-range", contents: "HOME=/home/\ufdd0\n", refuse: true},
		{name: "noncharacter-plane-end", contents: "HOME=/home/\U0010ffff\n", refuse: true},
		{name: "noncharacter-unrelated", contents: "PRIVATE_FIXTURE_SECRET=\ufffe\nHOME=/home\n", refuse: true},
		{name: "unrelated-malformed-quote", contents: "PRIVATE_FIXTURE_SECRET='unclosed\nHOME=/home\n", refuse: true},
		{name: "unrelated-multiline-single", contents: "HOME=/before\nPRIVATE_FIXTURE_SECRET='-----BEGIN FIXTURE-----\nHOME=/not-an-assignment\n-----END FIXTURE-----'\nXDG_STATE_HOME=/after\n", want: "HOME=/before\x00XDG_STATE_HOME=/after\x00"},
		{name: "unrelated-multiline-double", contents: "PRIVATE_FIXTURE_SECRET=\"-----BEGIN FIXTURE-----\nquoted \\\" marker\nXDG_STATE_HOME=/not-an-assignment\n-----END FIXTURE-----\"\nHOME=/after\n", want: "HOME=/after\x00"},
		{name: "unrelated-multiline-backslashes", contents: "PRIVATE_FIXTURE_SECRET=\"first\\\ncontinued\nlast\\\\\"\nHOME=/after\n", want: "HOME=/after\x00"},
		{name: "unrelated-multiline-large", contents: "PRIVATE_FIXTURE_SECRET='first\n" + strings.Repeat("x", 16385) + "\nlast'\nHOME=/after\n", want: "HOME=/after\x00"},
		{name: "unrelated-multiline-suffix", contents: "PRIVATE_FIXTURE_SECRET='first\nlast'garbage\nHOME=/after\n", refuse: true},
		{name: "multiline-config-home", contents: "XDG_CONFIG_HOME='/one\ntwo'\n", refuse: true},
		{name: "multiline-state-home", contents: "XDG_STATE_HOME=\"/one\ntwo\"\n", refuse: true},
		{name: "multiline-listener", contents: "EVENER_HUB_LISTEN_ADDR='127.0.0.1:\n0'\n", refuse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "env")
			if err := os.WriteFile(path, []byte(tc.contents), 0600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", systemdEnvironmentFileRemote(path, false))
			fetched, err := cmd.Output()
			if err != nil {
				t.Fatalf("file fetch failed: %v", err)
			}
			wantFetched := tc.contents
			if len(wantFetched) > 65537 {
				wantFetched = wantFetched[:65537]
			}
			if string(fetched) != wantFetched {
				t.Fatalf("file fetch altered bytes or exceeded its bound: got bytes=%d want=%d", len(fetched), len(wantFetched))
			}
			out, err := parseSystemdEnvironmentFile(fetched)
			if tc.refuse {
				if !errors.Is(err, errOwnerEvidence) || len(out) != 0 {
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
