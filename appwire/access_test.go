package appwire

import (
	"encoding/json"
	"testing"
)

func TestSessionAccessReadsThePersistedSandboxRequest(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name    string
		sandbox string
		network *bool
		want    ThreadAccess
	}{
		{"an unsandboxed session persists no mode", "", nil, ThreadAccess{Sandbox: "off", Network: true}},
		{"off is off whatever the network flag says", "off", &off, ThreadAccess{Sandbox: "off", Network: true}},
		{"a sandboxed session defaults to the network on", "workspace-write", nil, ThreadAccess{Sandbox: "workspace-write", Network: true}},
		{"the network on", "read-only", &on, ThreadAccess{Sandbox: "read-only", Network: true}},
		{"the network off", "restricted", &off, ThreadAccess{Sandbox: "restricted", Network: false}},
		{"surrounding space is not a mode", " workspace-write ", &off, ThreadAccess{Sandbox: "workspace-write", Network: false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := SessionAccess(tc.sandbox, tc.network)
			if got == nil || *got != tc.want {
				t.Fatalf("SessionAccess(%q, %v) = %+v, want %+v", tc.sandbox, tc.network, got, tc.want)
			}
		})
	}
}

// The network key is always present: false is the fact a restricted session
// reports, never an absent key a client could read as "not known".
func TestThreadAccessAlwaysCarriesBothKeys(t *testing.T) {
	raw, err := json.Marshal(EvenerThread{Access: &ThreadAccess{Sandbox: "restricted", Network: false}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Access map[string]any `json:"access"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Access["sandbox"] != "restricted" || decoded.Access["network"] != false {
		t.Fatalf("access = %v, want sandbox restricted and network false", decoded.Access)
	}
	raw, err = json.Marshal(EvenerThread{})
	if err != nil {
		t.Fatal(err)
	}
	var bare map[string]any
	if json.Unmarshal(raw, &bare) != nil || bare["access"] != nil {
		t.Fatalf("a thread without access encoded %s, want no access key", raw)
	}
}

func TestCloneThreadOwnsAccess(t *testing.T) {
	original := Thread{Evener: EvenerThread{Access: &ThreadAccess{Sandbox: "read-only", Network: true}}}
	clone := CloneThread(original)
	clone.Evener.Access.Sandbox = "changed"
	if original.Evener.Access.Sandbox != "read-only" {
		t.Fatal("the clone shares the original's access")
	}
}
