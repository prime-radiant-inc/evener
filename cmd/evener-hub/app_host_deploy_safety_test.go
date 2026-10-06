package hub

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubtestenv"
	"primeradiant.com/evener/cmd/evener-hub/internal/sshconn"
	"primeradiant.com/evener/cmdutil"
	"primeradiant.com/evener/execsupport/shellquote"
)

const hostDeployEnvironmentChildEnv = "EVENER_HOST_DEPLOY_ENV_CHILD"

func runHostDeployEnvironmentProbe() error {
	if err := cmdutil.EnsureUserConfigDirs(); err != nil {
		return err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return err
	}
	providers, _ := cmdutil.ProvidersConfigPath()
	paths := map[string]string{
		"home": os.Getenv("HOME"), "config": cmdutil.DefaultConfigRoot(),
		"state": cmdutil.DefaultStateRoot(), "cache": cache,
		"providers": providers, "credentials": cmdutil.CredentialsPath(),
		"state_override": os.Getenv("EVENER_STATE_DIR"),
	}
	for _, name := range []string{"state", "cache"} {
		if err := os.MkdirAll(paths[name], 0o700); err != nil {
			return err
		}
	}
	return json.NewEncoder(os.Stdout).Encode(paths)
}

func TestHostDeployRemoteEnvironment(t *testing.T) {
	for _, overrides := range []bool{false, true} {
		name := "home-defaults"
		if overrides {
			name = "inherited-overrides"
		}
		t.Run(name, func(t *testing.T) {
			normal := t.TempDir()
			private := filepath.Join(t.TempDir(), "private 'home;$literal")
			cacheDir := "home/cache"
			if runtime.GOOS == "darwin" {
				cacheDir = "home/Library/Caches"
			}
			command := hostDeployRemoteEnvironment(private) + shellquote.RemoteWord(os.Args[0]) + " -test.run=^TestHostDeployRemoteEnvironment$"
			cmd := exec.Command("sh", "-c", command)
			cmd.Env = append(os.Environ(), hostDeployEnvironmentChildEnv+"=1",
				"HOME="+normal, "XDG_CONFIG_HOME=", "XDG_STATE_HOME=", "XDG_CACHE_HOME=",
				"EVENER_PROVIDERS_CONFIG=", "EVENER_CREDENTIALS_CONFIG=", "EVENER_STATE_DIR=")
			if overrides {
				cmd.Env = append(cmd.Env, "XDG_CONFIG_HOME="+normal+"/config", "XDG_STATE_HOME="+normal+"/state", "XDG_CACHE_HOME="+normal+"/cache",
					"EVENER_PROVIDERS_CONFIG="+normal+"/providers.toml", "EVENER_CREDENTIALS_CONFIG="+normal+"/credentials.toml", "EVENER_STATE_DIR="+normal+"/project-state")
			}
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("remote environment subprocess: %v\n%s", err, out)
			}
			var got map[string]string
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode startup paths: %v\n%s", err, out)
			}
			for key, want := range map[string]string{
				"home": private + "/home", "config": private + "/home/config/evener",
				"state": private + "/home/state/evener", "cache": filepath.Join(private, cacheDir),
				"providers": private + "/home/config/evener/providers.toml", "credentials": private + "/home/config/evener/credentials.toml",
				"state_override": "",
			} {
				if got[key] != want {
					t.Errorf("startup %s = %q, want %q", key, got[key], want)
				}
			}
			for _, relative := range []string{"home/config/evener/skills", "home/config/evener/plugins", "home/state/evener", cacheDir} {
				if info, err := os.Stat(filepath.Join(private, relative)); err != nil || !info.IsDir() {
					t.Errorf("private startup directory %s missing: %v", relative, err)
				}
			}
			if entries, err := os.ReadDir(normal); err != nil || len(entries) != 0 {
				t.Errorf("startup touched the normal home: entries=%v error=%v", entries, err)
			}
		})
	}
}

const hostHashFailureChildEnv = "EVENER_HOST_HASH_FAILURE_CHILD"

func TestHostDeployConfigKeepsRuntimePathsPrivate(t *testing.T) {
	private := t.TempDir()
	configPath := filepath.Join(private, hostDeployToml)
	if err := os.WriteFile(configPath, hostDeployConfig(private, "127.0.0.1:19180"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfigExplicit(configPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"hub_state_root", cfg.HubStateRoot, filepath.Join(private, "state")},
		{"run_dir", cfg.RunDir, filepath.Join(private, "state", "run")},
		{"state_glob", cfg.StateGlob, filepath.Join(private, "state", "projects", "*")},
		{"past_index_db", cfg.PastIndexDB, filepath.Join(private, "state", "index.db")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("private hub config %s = %q, want %q; empty runtime paths use the host's normal defaults", tc.name, tc.got, tc.want)
			}
		})
	}
	if cfg.Addr != "127.0.0.1:19180" || cfg.PluginAutoUpgrade {
		t.Fatalf("private hub addr=%q auto-upgrade=%v", cfg.Addr, cfg.PluginAutoUpgrade)
	}
}

func TestHostDeployCatalogProjectFiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	host := &hostSSH{t: t, target: "invalid target", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", script)
	}}
	alpha := filepath.Join(root, "catalog alpha")
	beta := filepath.Join(root, "catalog beta")
	host.prepareCatalogProject(alpha, "alpha", "OPAQUE_ALPHA")
	host.prepareCatalogProject(beta, "beta", "OPAQUE_BETA")
	for _, tc := range []struct {
		cwd         string
		name        string
		description string
		excluded    string
	}{
		{alpha, "alpha", "OPAQUE_ALPHA", "beta"},
		{beta, "beta", "OPAQUE_BETA", "alpha"},
		{alpha, "alpha", "OPAQUE_ALPHA", "beta"},
	} {
		catalog, err := hubSpawnSlashCatalog(context.Background(), hubcore.WebConfig{}, appwire.SpawnSlashCatalogParams{CWD: tc.cwd, Harness: "evener"})
		if err != nil {
			t.Fatal(err)
		}
		assertDeployCatalogProject(t, catalog, tc.name, tc.description, tc.excluded)
	}
}

func TestHostHashFailureIsNotAbsence(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("OpenSSH is required to exercise its pre-connection hostname rejection")
	}
	if os.Getenv(hostHashFailureChildEnv) == "1" {
		// OpenSSH rejects whitespace in a hostname before any connection attempt.
		host := &hostSSH{t: t, target: "invalid target"}
		host.sha256IfFile("/unused/evener")
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestHostHashFailureIsNotAbsence$")
	cmd.Env = append(os.Environ(), hubtestenv.RootVar+"="+testEnv.Root, hostHashFailureChildEnv+"=1")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("the hash probe passed after a real SSH error, falsely proving absence:\n%s", out)
	}
	if !strings.Contains(string(out), "hostname contains invalid characters") {
		t.Fatalf("the child failed without the expected pre-connection SSH error: %v\n%s", err, out)
	}
}

const (
	hostSafetyChildCaseEnv = "EVENER_HOST_SAFETY_CHILD_CASE"
	hostSafetyChildRootEnv = "EVENER_HOST_SAFETY_CHILD_ROOT"
)

func TestHostDeployFilesystemSafety(t *testing.T) {
	if name := os.Getenv(hostSafetyChildCaseEnv); name != "" {
		runHostSafetyChild(t, name, os.Getenv(hostSafetyChildRootEnv))
		return
	}
	for _, name := range []string{"hash-file", "hash-stderr", "hash-absent", "hash-directory", "hash-symlink", "hash-empty-output", "hash-malformed-output", "hash-invalid-hex", "claim-existing", "claim-file", "claim-symlink", "claim-race", "cleanup-stop-error", "cleanup-probe-error", "cleanup-probe-unrecognized", "cleanup-cleared"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			private := filepath.Join(root, "private hub")
			if name == "hash-file" || name == "hash-stderr" || name == "hash-empty-output" || name == "hash-malformed-output" || name == "claim-file" {
				if err := os.WriteFile(private, []byte("abc"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "hash-directory" || name == "claim-existing" || strings.HasPrefix(name, "cleanup-") {
				if err := os.Mkdir(private, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(private, "keep"), []byte("owned fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if name == "hash-symlink" || name == "claim-symlink" {
				if err := os.Symlink(filepath.Join(root, "absent"), private); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestHostDeployFilesystemSafety$")
			cmd.Env = append(os.Environ(), hubtestenv.RootVar+"="+testEnv.Root, hostSafetyChildCaseEnv+"="+name, hostSafetyChildRootEnv+"="+root)
			out, err := cmd.CombinedOutput()
			switch name {
			case "hash-file", "hash-stderr", "hash-absent":
				if err != nil {
					t.Fatalf("valid hash/absence control failed: %v\n%s", err, out)
				}
			case "hash-directory", "hash-symlink", "hash-empty-output", "hash-malformed-output", "hash-invalid-hex", "claim-existing", "claim-file", "claim-symlink":
				if err == nil {
					t.Fatalf("the probe accepted an unproved result (%s): %s", name, out)
				}
				want := "hash " + private
				if strings.HasPrefix(name, "claim-") {
					want = "claim private directory " + private
				}
				if !strings.Contains(string(out), want) {
					t.Fatalf("the child failed without the expected %s error: %v\n%s", name, err, out)
				}
			case "claim-race":
				if _, statErr := os.Stat(filepath.Join(private, "keep")); statErr == nil {
					if err == nil {
						t.Fatalf("the deploy adopted a directory created by the competing process:\n%s", out)
					}
				} else if !os.IsNotExist(statErr) {
					t.Fatalf("inspect competing creator's file: %v", statErr)
				} else if err != nil {
					t.Fatalf("the deploy failed despite winning the directory claim: %v\n%s", err, out)
				} else {
					for _, child := range []string{"bin", "state"} {
						info, statErr := os.Stat(filepath.Join(private, child))
						if statErr != nil || !info.IsDir() {
							t.Fatalf("the successful claim did not prepare %s: %v", child, statErr)
						}
					}
				}
			case "cleanup-stop-error":
				if err == nil || !strings.Contains(string(out), "stopping the deploy check's host hub") {
					t.Fatalf("the failed stop was not reported: %v\n%s", err, out)
				}
				if got, readErr := os.ReadFile(filepath.Join(private, "keep")); readErr != nil || string(got) != "owned fixture" {
					t.Fatalf("cleanup removed the private directory after an unproved stop: bytes=%q error=%v\n%s", got, readErr, out)
				}
			case "cleanup-probe-error", "cleanup-probe-unrecognized":
				if err == nil || !strings.Contains(string(out), "shutdown on 127.0.0.1:19180 is unproved") {
					t.Fatalf("the unproved shutdown poll was not reported: %v\n%s", err, out)
				}
				if got, readErr := os.ReadFile(filepath.Join(private, "keep")); readErr != nil || string(got) != "owned fixture" {
					t.Fatalf("cleanup removed the private directory after an unproved poll: bytes=%q error=%v\n%s", got, readErr, out)
				}
			case "cleanup-cleared":
				if err != nil {
					t.Fatalf("proved shutdown cleanup failed: %v\n%s", err, out)
				}
				if _, statErr := os.Stat(private); !os.IsNotExist(statErr) {
					t.Fatalf("proved shutdown did not remove the private directory: %v", statErr)
				}
			}
			if name == "claim-existing" || name == "claim-race" {
				if _, statErr := os.Stat(filepath.Join(private, "keep")); statErr == nil {
					for _, child := range []string{"bin", "state"} {
						if _, childErr := os.Stat(filepath.Join(private, child)); !os.IsNotExist(childErr) {
							t.Fatalf("deploy modified another creator's directory (%s): %v", child, childErr)
						}
					}
				}
			}
			if name == "hash-symlink" || name == "claim-symlink" {
				if target, readErr := os.Readlink(private); readErr != nil || target != filepath.Join(root, "absent") {
					t.Fatalf("probe changed the dangling symlink: target=%q error=%v", target, readErr)
				}
			}
			if name == "claim-file" {
				if got, readErr := os.ReadFile(private); readErr != nil || string(got) != "abc" {
					t.Fatalf("claim changed the existing file: bytes=%q error=%v", got, readErr)
				}
			}
		})
	}
}

func runHostSafetyChild(t *testing.T, name, root string) {
	t.Helper()
	private := filepath.Join(root, "private hub")
	commands := 0
	host := &hostSSH{t: t, target: "local fixture", execCommand: func(ctx context.Context, script string) *exec.Cmd {
		commands++
		switch name {
		case "hash-stderr":
			script = "printf 'hash warning\\n' >&2; " + script
		case "hash-empty-output":
			script = "exit 0"
		case "hash-malformed-output":
			script = "printf 'not-a-sha256\\n'"
		case "hash-invalid-hex":
			script = "printf '%s\\n' " + shellquote.RemoteWord(strings.Repeat("z", 64))
		}
		if name == "claim-race" && commands == 1 {
			// A real competing creator runs after the first operation. An atomic
			// claim wins before it; a separate existence probe leaves it a window.
			script += "; status=$?; if mkdir " + shellquote.RemoteWord(private) + " 2>/dev/null; then printf competitor > " + shellquote.RemoteWord(filepath.Join(private, "keep")) + "; fi; exit $status"
		}
		if name == "cleanup-stop-error" {
			if commands == 1 {
				script = "exit 1"
			} else if script == sshconn.ListenerProbeRemote("19180") {
				script = "printf '%s\\n' " + shellquote.RemoteWord(sshconn.NoListenerMarker)
			}
		}
		if name == "cleanup-probe-error" || name == "cleanup-probe-unrecognized" || name == "cleanup-cleared" {
			if commands == 1 {
				script = "exit 0"
			} else if script == sshconn.ListenerProbeRemote("19180") {
				switch name {
				case "cleanup-probe-error":
					script = "printf '%s\\n' " + shellquote.RemoteWord(sshconn.NoListenerMarker) + "; exit 1"
				case "cleanup-probe-unrecognized":
					script = "printf 'unrecognized\\n'"
				case "cleanup-cleared":
					script = "printf '%s\\n' " + shellquote.RemoteWord(sshconn.NoListenerMarker)
				}
			}
		}
		return exec.CommandContext(ctx, "sh", "-c", script)
	}}
	switch name {
	case "hash-file", "hash-stderr":
		const want = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
		if got := host.sha256IfFile(private); got != want {
			t.Fatalf("hash of abc = %q, want %q", got, want)
		}
	case "hash-absent":
		if got := host.sha256IfFile(private); got != "" {
			t.Fatalf("absent file hash = %q", got)
		}
	case "hash-directory", "hash-symlink", "hash-empty-output", "hash-malformed-output", "hash-invalid-hex":
		host.sha256IfFile(private)
	case "claim-existing", "claim-file", "claim-symlink", "claim-race":
		host.prepareDeployDir(private)
	case "cleanup-stop-error", "cleanup-probe-error", "cleanup-probe-unrecognized", "cleanup-cleared":
		cleanupHostDeploy(t, host, private, "127.0.0.1:19180", filepath.Join(root, "absent install"), "")
	default:
		t.Fatalf("unknown host safety fixture %q", name)
	}
}
