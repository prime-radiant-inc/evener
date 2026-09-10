package appwire

import (
	"encoding/json"
	"testing"
)

func TestSpawnSlashCatalogParamsMatchThreadStartSpelling(t *testing.T) {
	data, err := json.Marshal(SpawnSlashCatalogParams{
		CWD:             "/repo",
		Harness:         "evener",
		LaunchOverrides: &LaunchConfigLayer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"cwd", "harness", "launchOverrides"} {
		if _, ok := raw[key]; !ok {
			t.Fatalf("params JSON = %v, missing %q", raw, key)
		}
	}
}

func TestSpawnSlashCatalogMethodCatalog(t *testing.T) {
	var found *MethodSpec
	for i := range Methods {
		if Methods[i].Name == MethodEvenerSpawnSlashCatalog {
			found = &Methods[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("method catalog missing %q", MethodEvenerSpawnSlashCatalog)
	}
	if found.Scope != ScopeHub {
		t.Fatalf("spawn slash catalog scope = %q, want %q", found.Scope, ScopeHub)
	}
	if _, ok := found.Params.(SpawnSlashCatalogParams); !ok {
		t.Fatalf("spawn slash catalog params type = %T, want SpawnSlashCatalogParams", found.Params)
	}
	if _, ok := found.Result.(SpawnSlashCatalogResponse); !ok {
		t.Fatalf("spawn slash catalog result type = %T, want SpawnSlashCatalogResponse", found.Result)
	}
}
