package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeAgentGateGo stands in for go under run-module-tests.sh with MODULES=agent.
// The agent-shards run and the subpackage go test each drop a marker, and each
// checks the other's to prove the order the gate ran them in:
//
//   - FAKE_EXPECT=alongside: the shards wait (up to a 30s tripwire) for the
//     subpackages to start, so a gate that runs the subpackages only after the
//     shards fails here instead of passing by luck.
//   - FAKE_EXPECT=after: the subpackages require the shards to have finished.
//
// FAKE_SUBPACKAGES_FAIL=1 makes the subpackage go test fail.
const fakeAgentGateGo = `#!/bin/sh
case "$1" in
list)
	echo primeradiant.com/evener/agent
	echo primeradiant.com/evener/agent/sub
	;;
run)
	case "$*" in
	*agent-shards*)
		: > "$FAKE_DIR/shards.started"
		if [ "$FAKE_EXPECT" = alongside ]; then
			i=0
			# TRIPWIRE: the gate starts the subpackages at once; this only bounds a gate that never does.
			while [ ! -f "$FAKE_DIR/subs.started" ]; do
				i=$((i + 1))
				if [ "$i" -gt 3000 ]; then
					echo "the shards finished without the subpackages ever starting"
					exit 1
				fi
				sleep 0.01
			done
		fi
		echo "PASS  agent:0   0.01s (3 tests)"
		: > "$FAKE_DIR/shards.done"
		;;
	esac
	;;
test)
	: > "$FAKE_DIR/subs.started"
	if [ "$FAKE_EXPECT" = after ] && [ ! -f "$FAKE_DIR/shards.done" ]; then
		echo "the subpackages started before the shards finished"
		exit 1
	fi
	if [ "${FAKE_SUBPACKAGES_FAIL:-0}" = 1 ]; then
		echo "FAIL	primeradiant.com/evener/agent/sub	0.01s"
		exit 1
	fi
	printf 'ok  \tprimeradiant.com/evener/agent/sub\t0.01s\n'
	;;
esac
exit 0
`

// runAgentGate runs the real gate script for the agent module alone against
// fakeAgentGateGo, returning its combined output and exit error.
func runAgentGate(t *testing.T, env ...string) (string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(fakeAgentGateGo), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("scripts/gate/run-module-tests.sh", "-short", "-count=1")
	cmd.Env = envOverride(os.Environ(), append([]string{
		"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_DIR=" + dir,
		"MODULES=agent",
		"WEB=0",
		"AGENT_SHARDS=1",
		"AGENT_SUBPACKAGES_ALONGSIDE=0",
		"GOFLAGS=",
		"GOTMPDIR=",
	}, env...)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestGateRunsAgentSubpackagesAfterItsShardsByDefault(t *testing.T) {
	if out, err := runAgentGate(t, "FAKE_EXPECT=after"); err != nil {
		t.Fatalf("gate: %v\n%s", err, out)
	}
}

func TestGateRunsAgentSubpackagesAlongsideItsShardsWhenAsked(t *testing.T) {
	if out, err := runAgentGate(t, "FAKE_EXPECT=alongside", "AGENT_SUBPACKAGES_ALONGSIDE=1"); err != nil {
		t.Fatalf("gate: %v\n%s", err, out)
	}
}

func TestGateFailsWhenAgentSubpackagesRunAlongsideFail(t *testing.T) {
	out, err := runAgentGate(t, "FAKE_EXPECT=alongside", "AGENT_SUBPACKAGES_ALONGSIDE=1", "FAKE_SUBPACKAGES_FAIL=1")
	if err == nil {
		t.Fatalf("gate passed with a failing agent subpackage run alongside the shards\n%s", out)
	}
	if !strings.Contains(out, "FAIL  agent") {
		t.Fatalf("gate failed without failing the agent module\n%s", out)
	}
}

func TestGateRefusesAnUnknownAgentSubpackagesAlongsideValue(t *testing.T) {
	out, err := runAgentGate(t, "FAKE_EXPECT=after", "AGENT_SUBPACKAGES_ALONGSIDE=yes")
	if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() != 2 {
		t.Fatalf("gate with AGENT_SUBPACKAGES_ALONGSIDE=yes: err = %v, want exit 2\n%s", err, out)
	}
	if !strings.Contains(out, "AGENT_SUBPACKAGES_ALONGSIDE") {
		t.Fatalf("refusal does not name the toggle\n%s", out)
	}
}
