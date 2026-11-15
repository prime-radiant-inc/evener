package hub

import (
	"slices"
	"testing"

	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
)

func provenanceArgValue(args []string, name string) (string, bool) {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

func TestBuildSpawnArgsCarriesCommitProvenance(t *testing.T) {
	args := buildSpawnArgs(hubcore.SpawnRequest{
		Resolved: launchconfig.Resolved{Effective: launchconfig.Layer{CommitProvenance: "off"}},
	})
	if got, ok := provenanceArgValue(args, "--commit-provenance"); !ok || got != "off" {
		t.Fatalf("args = %v, want --commit-provenance off", args)
	}
}

func TestBuildResumeArgsKeepsSavedCommitProvenance(t *testing.T) {
	args := buildResumeArgs(hubcore.ResumeRequest{
		SessionID: "sess",
		Resolved: launchconfig.Resolved{
			Effective:  launchconfig.Layer{CommitProvenance: "off"},
			Provenance: map[string]launchconfig.LayerName{"commit_provenance": launchconfig.LayerGlobal},
		},
	})
	if slices.Contains(args, "--commit-provenance") {
		t.Fatalf("global provenance leaked into resume args: %v", args)
	}
}

func TestBuildResumeArgsPassesExplicitCommitProvenance(t *testing.T) {
	args := buildResumeArgs(hubcore.ResumeRequest{
		SessionID: "sess",
		Resolved: launchconfig.Resolved{
			Effective:  launchconfig.Layer{CommitProvenance: "off"},
			Provenance: map[string]launchconfig.LayerName{"commit_provenance": launchconfig.LayerLaunch},
		},
	})
	if got, ok := provenanceArgValue(args, "--commit-provenance"); !ok || got != "off" {
		t.Fatalf("args = %v, want the explicit --commit-provenance off", args)
	}
}

func TestBuildResumeArgsKeepsSavedCompactAtPercent(t *testing.T) {
	for _, layer := range []launchconfig.LayerName{launchconfig.LayerGlobal, launchconfig.LayerProject, ""} {
		args := buildResumeArgs(hubcore.ResumeRequest{
			SessionID: "sess",
			Resolved: launchconfig.Resolved{
				Effective:  launchconfig.Layer{CompactAtPercent: new(90)},
				Provenance: map[string]launchconfig.LayerName{"compact_at_percent": layer},
			},
		})
		if slices.Contains(args, "--compact-at-percent") {
			t.Fatalf("layer %q: default or inherited percent leaked into resume args: %v", layer, args)
		}
	}
}

func TestBuildResumeArgsPassesExplicitCompactAtPercent(t *testing.T) {
	args := buildResumeArgs(hubcore.ResumeRequest{
		SessionID: "sess",
		Resolved: launchconfig.Resolved{
			Effective:  launchconfig.Layer{CompactAtPercent: new(60)},
			Provenance: map[string]launchconfig.LayerName{"compact_at_percent": launchconfig.LayerLaunch},
		},
	})
	if got, ok := provenanceArgValue(args, "--compact-at-percent"); !ok || got != "60" {
		t.Fatalf("args = %v, want the explicit --compact-at-percent 60", args)
	}
}
