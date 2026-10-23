package launchcheck

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestHubEndpointCapability(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := RunLaunchCheck([]string{"--json"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Schema int `json:"hub_endpoint_schema"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Schema != 1 {
		t.Fatalf("hub_endpoint_schema=%d, want 1", result.Schema)
	}
}
