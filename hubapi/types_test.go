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
