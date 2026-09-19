package hub

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/interactiveartifacts"
)

func TestDaemonBrokerLaunchAppendsDescriptorsWithoutExposingCorrelation(t *testing.T) {
	authority, err := interactiveartifacts.OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close() //nolint:errcheck

	existingRead, existingWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer existingRead.Close()             //nolint:errcheck
	defer existingWrite.Close()            //nolint:errcheck
	cmd := exec.Command("evener", "serve") //nolint:gosec // construction only; this test never starts it
	cmd.Env = []string{"SAFE=value"}
	cmd.ExtraFiles = []*os.File{existingRead, existingWrite}

	launch, err := attachDaemonBrokerLaunch(cmd, daemonBrokerLaunchConfig{
		Authority: authority,
		ProjectID: "project-0123456789",
		HubEpoch:  "hub-epoch",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer launch.close() //nolint:errcheck

	if got := len(cmd.ExtraFiles); got != 4 {
		t.Fatalf("ExtraFiles length = %d, want 4", got)
	}
	wantReadFD := 3 + 2
	wantWriteFD := wantReadFD + 1
	if !slices.Contains(cmd.Args, "--artifact-broker-read-fd="+strconv.Itoa(wantReadFD)) || !slices.Contains(cmd.Args, "--artifact-broker-write-fd="+strconv.Itoa(wantWriteFD)) {
		t.Fatalf("broker descriptor flags missing actual appended positions: %v", cmd.Args)
	}
	for _, exposed := range append(slices.Clone(cmd.Args), cmd.Env...) {
		if strings.Contains(exposed, launch.launchID) {
			t.Fatalf("launch correlation exposed outside private IPC: %q", exposed)
		}
	}
	if cmd.ExtraFiles[0] != existingRead || cmd.ExtraFiles[1] != existingWrite {
		t.Fatal("broker attachment replaced fixture-owned ExtraFiles")
	}
}
