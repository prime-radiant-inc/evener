package remoteinstall

import (
	"bytes"
	"os"
	"strconv"
	"testing"
)

func TestEmbeddedScriptMatchesReviewedInstallers(t *testing.T) {
	for _, path := range []string{"../../install.sh", "../../cmd/evener-hub/internal/sshconn/install.sh"} {
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(Script, want) {
			t.Fatalf("embedded installer differs from %s", path)
		}
	}
}

func TestCommandChecksTheEmbeddedScriptSize(t *testing.T) {
	command := Command("snapshot", "/tmp/evener;do-not-run", "", "")
	if !bytes.Contains([]byte(command), []byte("[ \"$v\" = "+strconv.Itoa(len(Script)))) {
		t.Fatalf("command does not check the embedded script size: %q", command)
	}
	if !bytes.Contains([]byte(command), []byte("PREFIX='/tmp/evener;do-not-run'")) {
		t.Fatalf("command does not quote PREFIX: %q", command)
	}
}
