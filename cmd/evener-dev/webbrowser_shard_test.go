package dev

import (
	"os"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

// CI runs the browser gate one shard per runner, so every guard belongs to
// exactly one shard: a guard in none would never run in CI, and a guard in two
// would run twice.
func TestBrowserGuardShardsPartitionTheGuards(t *testing.T) {
	owner := map[string]string{}
	for shard, guards := range browserGuardShards {
		if len(guards) == 0 {
			t.Errorf("shard %q has no guards", shard)
		}
		for _, guard := range guards {
			if !slices.Contains(browserGuards, guard) {
				t.Errorf("shard %q names %q, which is not a browser guard", shard, guard)
			}
			if other, taken := owner[guard]; taken {
				t.Errorf("%q is in shards %q and %q", guard, other, shard)
			}
			owner[guard] = shard
		}
	}
	for _, guard := range browserGuards {
		if _, ok := owner[guard]; !ok {
			t.Errorf("%q is in no shard, so CI never runs it", guard)
		}
	}
}

// No shard is the whole gate; a shard is its own guards in verdict order; a
// name that is no shard is refused rather than running nothing.
func TestBrowserShardGuards(t *testing.T) {
	all, err := browserShardGuards("")
	if err != nil || !slices.Equal(all, browserGuards) {
		t.Fatalf("no shard = %q, %v; want every guard %q", all, err, browserGuards)
	}
	for shard, members := range browserGuardShards {
		got, err := browserShardGuards(shard)
		if err != nil {
			t.Fatalf("shard %q: %v", shard, err)
		}
		if len(got) != len(members) {
			t.Errorf("shard %q = %q, want its members %q", shard, got, members)
		}
		last := -1
		for _, guard := range got {
			if !slices.Contains(members, guard) {
				t.Errorf("shard %q ran %q, which is not one of its guards", shard, guard)
			}
			at := slices.Index(browserGuards, guard)
			if at <= last {
				t.Errorf("shard %q = %q is out of verdict order", shard, got)
			}
			last = at
		}
	}
	if got, err := browserShardGuards("no-such-shard"); err == nil {
		t.Fatalf("an unknown shard ran %q; want it refused", got)
	}
}

// The web-browser CI job's matrix names every shard, and only shards: a shard
// it left out would never run, and one it invented would fail the lane.
func TestCIWebBrowserMatrixRunsEveryShard(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Strategy struct {
				Matrix struct {
					Shard []string `yaml:"shard"`
				} `yaml:"matrix"`
			} `yaml:"strategy"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	got := slices.Sorted(slices.Values(workflow.Jobs["web-browser"].Strategy.Matrix.Shard))
	var want []string
	for shard := range browserGuardShards {
		want = append(want, shard)
	}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("web-browser matrix shards = %q, want %q", got, want)
	}
}
