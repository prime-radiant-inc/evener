package apilog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAPIAttemptRequestPrunedFieldsJSON(t *testing.T) {
	raw, err := json.Marshal(APIAttemptRequest{Method: "POST", Endpoint: "https://x/v1", PrunedFields: []string{"store"}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"pruned_fields":["store"]`) {
		t.Fatalf("json = %s", raw)
	}
	raw, _ = json.Marshal(APIAttemptRequest{Method: "POST", Endpoint: "https://x/v1"})
	if strings.Contains(string(raw), "pruned_fields") {
		t.Fatalf("empty pruned_fields must be omitted: %s", raw)
	}
}
