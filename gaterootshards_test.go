package evener_test

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// gateRootShardsLib holds the decisions the gate makes about the root module's
// sharded packages: which run as shards beside the root go test, which run
// inside it, and which another job runs (so this one leaves them out).
const gateRootShardsLib = "scripts/lib/gate-root-shards.sh"

// runRootShardsCase sources the library in bash with env set and runs script,
// returning its combined output and exit error.
func runRootShardsCase(t *testing.T, env []string, script string) (string, error) {
	t.Helper()
	if _, err := os.Stat(gateRootShardsLib); err != nil {
		t.Fatalf("stat %s: %v", gateRootShardsLib, err)
	}
	cmd := exec.Command("bash", "-c", "set -uo pipefail\n. "+gateRootShardsLib+"\n"+script)
	cmd.Env = envOverride(append([]string{"PATH=" + os.Getenv("PATH"), "LC_ALL=C"}, env...))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestRootShardModeReadsEachPackagesToggle(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  string
	}{
		{"", "shard"},
		{"1", "shard"},
		{"0", "inline"},
		{"elsewhere", "elsewhere"},
	} {
		env := []string{}
		if tc.value != "" {
			env = append(env, "HUB_SHARDS="+tc.value)
		}
		got, err := runRootShardsCase(t, env, `root_shard_mode HUB`)
		if err != nil || got != tc.want {
			t.Errorf("HUB_SHARDS=%q: root_shard_mode = %q, %v; want %q", tc.value, got, err, tc.want)
		}
	}
}

// A mistyped toggle must not silently fall back to some mode: a race job that
// meant "elsewhere" but ran the hub anyway would double its work, and one that
// meant "1" but dropped the hub would stop testing it.
func TestRootShardModeRefusesAnUnknownValue(t *testing.T) {
	got, err := runRootShardsCase(t, []string{"HUB_SHARDS=yes"}, `root_shard_mode HUB`)
	if err == nil || !strings.Contains(got, "HUB_SHARDS") {
		t.Fatalf("HUB_SHARDS=yes: root_shard_mode = %q, %v; want a refusal naming HUB_SHARDS", got, err)
	}
}

func TestRootShardsLeavesOutEveryPackageItDoesNotRunInline(t *testing.T) {
	got, err := runRootShardsCase(t, []string{"HUB_SHARDS=elsewhere", "CLI_SHARDS=1"}, `root_shard_excluded_packages`)
	if err != nil {
		t.Fatalf("root_shard_excluded_packages: %v: %s", err, got)
	}
	want := "primeradiant.com/evener/cmd/evener-hub\nprimeradiant.com/evener/cmd/evener"
	if got != want {
		t.Fatalf("excluded = %q, want %q", got, want)
	}
	got, err = runRootShardsCase(t, []string{"HUB_SHARDS=0", "CLI_SHARDS=0"}, `root_shard_excluded_packages`)
	if err != nil || got != "" {
		t.Fatalf("with both inline, excluded = %q, %v; want none", got, err)
	}
}

func TestRootShardsRunsOnlyTheShardedRunners(t *testing.T) {
	got, err := runRootShardsCase(t, []string{"HUB_SHARDS=1", "CLI_SHARDS=elsewhere"}, `root_shard_runners`)
	if err != nil || got != "hub HUB" {
		t.Fatalf("runners = %q, %v; want only %q", got, err, "hub HUB")
	}
}

func TestRootRestFollowsItsToggle(t *testing.T) {
	for _, tc := range []struct {
		value string
		runs  bool
	}{{"", true}, {"1", true}, {"0", false}} {
		env := []string{}
		if tc.value != "" {
			env = append(env, "ROOT_REST="+tc.value)
		}
		_, err := runRootShardsCase(t, env, `root_rest_enabled`)
		if (err == nil) != tc.runs {
			t.Errorf("ROOT_REST=%q: root_rest_enabled = %v; want runs=%v", tc.value, err, tc.runs)
		}
	}
	if got, err := runRootShardsCase(t, []string{"ROOT_REST=no"}, `root_rest_enabled`); err == nil || !strings.Contains(got, "ROOT_REST") {
		t.Fatalf("ROOT_REST=no = %q, %v; want a refusal naming ROOT_REST", got, err)
	}
}
