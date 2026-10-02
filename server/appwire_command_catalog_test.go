package server

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestAppDiagnosticsOwningCommandInventory(t *testing.T) {
	for _, commands := range [][]appwire.CommandDescriptor{nil, {}, {{Name: "project-only", Source: "project"}}} {
		ds := DetailedStatus{Commands: commands}
		raw, err := json.Marshal(ds)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"commands"`) != (commands != nil) {
			t.Fatalf("detailed inventory presence = %s", raw)
		}
		d := appDiagnosticsFromDetailedStatus(ds)
		cloned := appwire.CloneEvenerDiagnostics(d)
		raw, err = json.Marshal(cloned)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"commands"`) != (commands != nil) {
			t.Fatalf("wire cloned inventory presence = %s", raw)
		}
		if len(commands) != 0 {
			cloned.Commands[0].Name = "mutated"
			d.Commands[0].Name = "other mutation"
			if ds.Commands[0].Name != "project-only" {
				t.Fatal("diagnostic snapshots alias loaded inventory")
			}
		}
		if appStatusDiagnosticsFromDetailedStatus(ds).Commands != nil {
			t.Fatal("status-only probe must not carry completion inventory")
		}
	}
}
