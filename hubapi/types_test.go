package hubapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMobileAPIHealthContract(t *testing.T) {
	if MobileAPIVersion != 1 {
		t.Fatalf("MobileAPIVersion = %d, want 1", MobileAPIVersion)
	}
	data, err := json.Marshal(HealthResponse{MobileAPIVersion: MobileAPIVersion})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"mobile_api_version":1`) {
		t.Fatalf("health JSON missing mobile API version: %s", data)
	}
}

func TestSourceThreadStartIdempotencyJSON(t *testing.T) {
	withoutFlag, err := json.Marshal(Source{ID: "remote-a", Label: "remote-a", Kind: "appwire", Online: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(withoutFlag), "threadStartIdempotency") {
		t.Fatalf("expected omitempty on false ThreadStartIdempotency, got %s", withoutFlag)
	}
	withFlag, err := json.Marshal(Source{ID: "remote-a", Label: "remote-a", Kind: "appwire", Online: true, ThreadStartIdempotency: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(withFlag), `"threadStartIdempotency":true`) {
		t.Fatalf("expected threadStartIdempotency:true in %s", withFlag)
	}
}
