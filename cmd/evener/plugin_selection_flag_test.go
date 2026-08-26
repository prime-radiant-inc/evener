package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/plugins"
)

func TestPluginSelectionFlagPresence(t *testing.T) {
	var f pluginSelectionFlag
	if f.Value() != nil {
		t.Fatal("zero value must be omitted")
	}
	if err := f.Set(""); err != nil {
		t.Fatal(err)
	}
	got := f.Value()
	if got == nil || len(*got) != 0 {
		t.Fatalf("empty = %#v", got)
	}
	if err := f.Set(" alpha, beta "); err != nil {
		t.Fatal(err)
	}
	if diff := reflect.DeepEqual([]string{"alpha", "beta"}, *f.Value()); !diff {
		t.Fatalf("names = %#v", *f.Value())
	}
	*f.Value() = append(*f.Value(), "mutated")
	if got := f.String(); got != "alpha,beta" {
		t.Fatalf("String = %q", got)
	}
}

func TestPluginSelectionFlagRejectsMalformedNames(t *testing.T) {
	for _, raw := range []string{"alpha,,beta", ",alpha", "alpha,", "Alpha", "alpha_beta", "alpha--beta", "alpha beta"} {
		t.Run(raw, func(t *testing.T) {
			var f pluginSelectionFlag
			if err := f.Set(raw); err == nil {
				t.Fatalf("Set(%q) succeeded", raw)
			}
		})
	}
}

func TestPluginSelectionFlagKeepsDuplicateNamesForResolver(t *testing.T) {
	var f pluginSelectionFlag
	if err := f.Set("alpha, alpha"); err != nil {
		t.Fatal(err)
	}
	if got := f.Value(); !reflect.DeepEqual(*got, []string{"alpha", "alpha"}) {
		t.Fatalf("names = %#v", *got)
	}
}

func TestPluginSelectionResumeConflicts(t *testing.T) {
	empty := []string{}
	for _, test := range []struct {
		name       string
		resume     string
		resumeLast bool
		wantErr    bool
	}{
		{name: "resume", resume: "session", wantErr: true},
		{name: "resume-last", resumeLast: true, wantErr: true},
		{name: "resume-with", wantErr: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := rejectPluginSelectionWithResume(&empty, test.resume, test.resumeLast)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr=%v", err, test.wantErr)
			}
		})
	}
	if err := rejectPluginSelectionWithResume(nil, "session", true); err != nil {
		t.Fatalf("omitted selection rejected: %v", err)
	}
}

func TestRenderEffectivePluginListJSON(t *testing.T) {
	resolution := plugins.LaunchPluginResolution{
		Candidates: []plugins.LaunchPluginCandidate{{
			Name: "alpha", Version: "1.2.3", Description: "Alpha", Source: plugins.LaunchPluginSourceInstalled,
			Marketplace: "official", Path: "/plugins/alpha", SkillCount: 1, AgentCount: 2,
			CommandCount: 3, HookCount: 4, MCPCount: 5,
		}},
		Diagnostics: []plugins.LaunchPluginDiagnostic{{Name: "broken", Message: "invalid", Source: plugins.LaunchPluginSourceInstalled}},
	}
	var out bytes.Buffer
	if err := renderEffectivePluginList(&out, resolution, true); err != nil {
		t.Fatal(err)
	}
	var got effectivePluginListJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Plugins) != 1 || got.Plugins[0].Name != "alpha" || got.Plugins[0].SkillCount != 1 || got.Plugins[0].MCPCount != 5 {
		t.Fatalf("plugins = %+v", got.Plugins)
	}
	if len(got.Diagnostics) != 1 || !strings.Contains(got.Diagnostics[0].Message, "invalid") {
		t.Fatalf("diagnostics = %+v", got.Diagnostics)
	}
}
