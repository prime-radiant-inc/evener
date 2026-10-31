package hub

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestManagerAcceptanceRequiresSSHOptIns(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, ssh, deploy string
	}{
		{name: "default"},
		{name: "read only", ssh: "1"},
		{name: "deploy only", deploy: "1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			marker := filepath.Join(dir, "attempted")
			if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte("#!/bin/sh\n: > \"$(dirname \"$0\")/attempted\"\nexit 1\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("EVENER_SSH_E2E", tc.ssh)
			t.Setenv("EVENER_SSH_E2E_DEPLOY", tc.deploy)
			output, runErr := exec.Command(executable, "-test.run=^TestE2E_ManagerAcceptance$", "-test.v").CombinedOutput()
			if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("SSH attempted without both opt-ins: marker error=%v", err)
			}
			if runErr != nil {
				t.Fatalf("default acceptance invocation failed: %v\n%s", runErr, output)
			}
		})
	}
}
