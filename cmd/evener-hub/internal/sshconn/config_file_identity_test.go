package sshconn

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/execsupport/shellquote"
)

func TestConfigFileIdentityAssociation(t *testing.T) {
	for _, name := range []string{"hard-link", "symlink", "distinct-same-content", "missing", "invalid-path", "invalid-canonical"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			canonical := filepath.Join(root, "selected.toml")
			if name == "distinct-same-content" {
				canonical += " "
			}
			if name == "invalid-canonical" {
				canonical += "\nambiguous"
			}
			alias := filepath.Join(root, "hard ' linked.toml")
			if err := os.WriteFile(canonical, []byte("same config\n"), 0600); err != nil {
				t.Fatal(err)
			}
			want := name == "hard-link" || name == "symlink"
			switch name {
			case "hard-link", "invalid-canonical":
				if err := os.Link(canonical, alias); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(canonical, alias); err != nil {
					t.Fatal(err)
				}
			case "distinct-same-content":
				// A trailing space is pathname data, not framing or padding.
				alias = strings.TrimSuffix(canonical, " ")
				if err := os.WriteFile(alias, []byte("same config\n"), 0600); err != nil {
					t.Fatal(err)
				}
			case "invalid-path":
				alias += "\nambiguous"
				if err := os.WriteFile(alias, []byte("same config\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, site := range []string{"endpoint-selector", "endpoint-record", "owner-argv", "owner-report"} {
				t.Run(site, func(t *testing.T) {
					host, _ := ownerCommands(t)
					host.ConfigPath = canonical
					selected := remoteEndpointFixture(canonical, "127.0.0.1:19371", "boot-one")
					report := selected
					argv := []string{"/opt/evener", "hub", "--config=" + canonical}
					switch site {
					case "endpoint-selector":
						host.ConfigPath = alias
					case "endpoint-record":
						// Running is a pointer, so give this report its own record.
						report = remoteEndpointFixture(canonical, "127.0.0.1:19371", "boot-one")
						report.Running.ConfigPath = alias
						if name == "distinct-same-content" {
							host.ConfigPath = alias
						}
					case "owner-argv":
						argv[2] = "--config=" + alias
					case "owner-report":
						report = remoteEndpointFixture(alias, "127.0.0.1:19371", "boot-one")
					}
					prefix := ownerContext(root, []string{"HOME=" + root})
					endpoint := "/opt/evener hub endpoint --json --config " + shellquote.RemoteWord(strings.TrimSpace(host.ConfigPath))
					ownerEndpoint := prefix + "/opt/evener hub endpoint --json --config " + shellquote.RemoteWord(strings.TrimPrefix(argv[2], "--config="))
					fr := &fakeRunner{runFn: func(ctx context.Context, args []string, _ io.Reader) ([]byte, error) {
						remote := strings.Join(args[12:], " ")
						if remote == endpoint || remote == ownerEndpoint {
							return []byte(mustOwnerJSON(t, report)), nil
						}
						for _, path := range []string{canonical, alias} {
							stat := "stat -Lc '%d %i' " + shellquote.RemoteWord(path) + " 2>/dev/null || stat -L -f '%d %i' " + shellquote.RemoteWord(path)
							for _, context := range []string{"", prefix} {
								if remote == context+resolveDeployCommand(path) || remote == context+stat {
									return exec.CommandContext(ctx, "/bin/sh", "-c", remote).Output()
								}
							}
						}
						t.Fatalf("unexpected remote command: %s", remote)
						return nil, errOwnerEvidence
					}}
					m := newTestManager(t, testRegistry(t, host), fr, Options{})
					var err error
					if strings.HasPrefix(site, "endpoint-") {
						_, err = m.resolveHostEndpoint(t.Context(), host, selectedRestartFacts())
						if !want && !errors.Is(err, ErrPreflightDecode) {
							t.Fatalf("endpoint error classification: %v", err)
						}
					} else {
						err = m.checkOwnerConfig(t.Context(), host, argv, root, []string{"HOME=" + root}, selected)
					}
					if (err == nil) != want {
						t.Fatalf("accepted=%v want=%v err=%v", err == nil, want, err)
					}
				})
			}
		})
	}
}

func TestConfigFileIdentityStatEvidence(t *testing.T) {
	for _, value := range []string{"", "1", "1 2 3", "device 2", "1 inode", "1 -2", "1 2\n1 2\n"} {
		t.Run(strings.ReplaceAll(value, "\n", "_"), func(t *testing.T) {
			host, _ := ownerCommands(t)
			fr := &fakeRunner{runFn: func(_ context.Context, argv []string, _ io.Reader) ([]byte, error) {
				remote := strings.Join(argv[12:], " ")
				if remote == resolveDeployCommand("/srv/alias.toml") {
					return []byte("/srv/alias.toml\n"), nil
				}
				if remote != "stat -Lc '%d %i' /srv/alias.toml 2>/dev/null || stat -L -f '%d %i' /srv/alias.toml" {
					t.Fatalf("unexpected remote command: %s", remote)
				}
				return []byte(value), nil
			}}
			m := newTestManager(t, testRegistry(t, host), fr, Options{})
			if same, err := m.configPathMatches(t.Context(), host, "/srv/alias.toml", "/srv/selected.toml", ""); same || !errors.Is(err, errOwnerEvidence) {
				t.Fatalf("same=%v err=%v", same, err)
			}
		})
	}
}

func TestConfigFileIdentityBSDFallback(t *testing.T) {
	root := t.TempDir()
	canonical, alias, log := filepath.Join(root, "canonical.toml"), filepath.Join(root, "alias.toml"), filepath.Join(root, "stat-calls")
	if err := os.WriteFile(canonical, []byte("config\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(canonical, alias); err != nil {
		t.Fatal(err)
	}
	// Execute the exact fallback command with a modeled BSD stat front end.
	// Its successful branch delegates device/inode lookup to the native stat.
	stub := "stat() { printf '%s\\n' \"$1\" >> " + shellquote.RemoteWord(log) + "; [ \"$1\" != -Lc ] || return 1; [ \"$#\" -eq 4 ] && [ \"$1\" = -L ] && [ \"$2\" = -f ] && [ \"$3\" = '%d %i' ] || return 1; command stat -Lc \"$3\" \"$4\" 2>/dev/null || command stat -L -f \"$3\" \"$4\"; }; "
	host, _ := ownerCommands(t)
	fr := &fakeRunner{runFn: func(ctx context.Context, argv []string, _ io.Reader) ([]byte, error) {
		remote := strings.Join(argv[12:], " ")
		for _, path := range []string{alias, canonical} {
			quoted := shellquote.RemoteWord(path)
			if remote == resolveDeployCommand(path) || remote == "stat -Lc '%d %i' "+quoted+" 2>/dev/null || stat -L -f '%d %i' "+quoted {
				return exec.CommandContext(ctx, "/bin/sh", "-c", stub+remote).Output()
			}
		}
		t.Fatalf("unexpected remote command: %s", remote)
		return nil, errOwnerEvidence
	}}
	m := newTestManager(t, testRegistry(t, host), fr, Options{})
	if same, err := m.configPathMatches(t.Context(), host, alias, canonical, ""); !same || err != nil {
		t.Fatalf("same=%v err=%v", same, err)
	}
	calls, err := os.ReadFile(log)
	if err != nil || string(calls) != "-Lc\n-L\n-Lc\n-L\n" {
		t.Fatalf("stat calls=%q err=%v", calls, err)
	}
}
