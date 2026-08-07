//go:build serffuzz

package sandbox

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// FuzzSandboxProbe covers capability discovery through a scripted host boundary.
// It exercises success and failure outcomes without consulting the real OS,
// filesystem, PATH, kernel, or any executable.
func FuzzSandboxProbe(f *testing.F) {
	for _, seed := range []byte{0, 1, 2, 3, 7, 255} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, variant byte) {
		fake := scriptedProbeSystem{
			osName:          []string{"linux", "darwin", "windows"}[int(variant)%3],
			home:            "/fixture/home",
			bwrapPath:       "/fixture/bwrap",
			bwrapFound:      variant&1 == 0,
			bwrapRunErr:     nil,
			bwrapHelp:       []byte("usage --overlay-src\n"),
			kernel:          []byte("fixture-kernel\r\n"),
			seatbeltPresent: variant&2 == 0,
		}
		if variant&4 != 0 {
			fake.homeErr = errors.New("home unavailable")
		}
		if variant&8 != 0 {
			fake.bwrapRunErr = errors.New("user namespace unavailable")
		}
		if variant&16 != 0 {
			fake.bwrapHelp = []byte("usage without overlays\n")
		}
		if variant&32 != 0 {
			fake.bwrapHelpErr = errors.New("help unavailable")
		}
		if variant&64 != 0 {
			fake.kernelErr = errors.New("uname unavailable")
		}

		facts := (RealProber{system: fake}).Probe()
		if facts.OS != fake.osName {
			t.Fatalf("probe OS = %q, want %q", facts.OS, fake.osName)
		}
		if fake.homeErr != nil && facts.Home != "" {
			t.Fatalf("failed home lookup leaked %q", facts.Home)
		}
		if fake.homeErr == nil && facts.Home != fake.home {
			t.Fatalf("probe home = %q, want %q", facts.Home, fake.home)
		}
		if fake.bwrapFound {
			if facts.BwrapPath != fake.bwrapPath {
				t.Fatalf("bwrap path = %q, want %q", facts.BwrapPath, fake.bwrapPath)
			}
			if facts.BwrapCapable != (fake.bwrapRunErr == nil) {
				t.Fatalf("bwrap capability = %v, run error = %v", facts.BwrapCapable, fake.bwrapRunErr)
			}
			if facts.OverlaySupported != (fake.bwrapHelpErr == nil && string(fake.bwrapHelp) == "usage --overlay-src\n") {
				t.Fatalf("overlay capability = %v, help = %q err = %v", facts.OverlaySupported, fake.bwrapHelp, fake.bwrapHelpErr)
			}
		} else if facts.BwrapPath != "" || facts.BwrapCapable || facts.OverlaySupported {
			t.Fatalf("missing bwrap produced facts %+v", facts)
		}
		if fake.kernelErr != nil && facts.KernelVersion != "" {
			t.Fatalf("failed uname leaked %q", facts.KernelVersion)
		}
		if fake.kernelErr == nil && facts.KernelVersion != "fixture-kernel" {
			t.Fatalf("kernel version = %q", facts.KernelVersion)
		}
		if fake.osName == "darwin" && fake.seatbeltPresent {
			if facts.SandboxExecPath != "/usr/bin/sandbox-exec" {
				t.Fatalf("darwin seatbelt path = %q", facts.SandboxExecPath)
			}
		} else if facts.SandboxExecPath != "" {
			t.Fatalf("non-darwin/unavailable seatbelt path = %q", facts.SandboxExecPath)
		}
		fuzzHostProbeAdapterNoProcess(t)
	})
}

// fuzzHostProbeAdapterNoProcess touches only test-owned paths. The attempted
// command names do not exist, so exec.CommandContext fails before a child can
// start; this covers the real adapter's construction path without a host probe.
func fuzzHostProbeAdapterNoProcess(t *testing.T) {
	t.Helper()
	host := hostProbeSystem{}
	if host.goos() == "" {
		t.Fatal("host probe adapter returned an empty OS")
	}
	root := t.TempDir()
	file := filepath.Join(root, "present-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write probe adapter fixture: %v", err)
	}
	if !host.nonDirectoryFile(file) || host.nonDirectoryFile(root) {
		t.Fatal("host probe adapter misclassified test-owned file/directory")
	}
	missing := filepath.Join(root, "missing-command")
	if _, err := host.lookPath(missing); err == nil {
		t.Fatal("host probe adapter resolved a missing absolute executable")
	}
	ctx := context.Background()
	if err := host.run(ctx, missing); err == nil {
		t.Fatal("host probe adapter started a missing executable")
	}
	if _, err := host.combinedOutput(ctx, missing); err == nil {
		t.Fatal("host probe adapter collected output from a missing executable")
	}
	if _, err := host.output(ctx, missing); err == nil {
		t.Fatal("host probe adapter collected stdout from a missing executable")
	}
}

type scriptedProbeSystem struct {
	osName          string
	home            string
	homeErr         error
	bwrapPath       string
	bwrapFound      bool
	bwrapRunErr     error
	bwrapHelp       []byte
	bwrapHelpErr    error
	kernel          []byte
	kernelErr       error
	seatbeltPresent bool
	env             map[string]string
}

func (s scriptedProbeSystem) goos() string { return s.osName }

func (s scriptedProbeSystem) getenv(name string) string { return s.env[name] }

func (s scriptedProbeSystem) userHomeDir() (string, error) { return s.home, s.homeErr }

func (s scriptedProbeSystem) lookPath(name string) (string, error) {
	if name == "bwrap" && s.bwrapFound {
		return s.bwrapPath, nil
	}
	return "", errors.New("not found")
}

func (s scriptedProbeSystem) nonDirectoryFile(path string) bool {
	return path == "/usr/bin/sandbox-exec" && s.seatbeltPresent
}

func (s scriptedProbeSystem) run(_ context.Context, _ string, _ ...string) error {
	return s.bwrapRunErr
}

func (s scriptedProbeSystem) combinedOutput(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return s.bwrapHelp, s.bwrapHelpErr
}

func (s scriptedProbeSystem) output(_ context.Context, _ string, _ ...string) ([]byte, error) {
	return s.kernel, s.kernelErr
}
