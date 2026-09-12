package hub

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubThreadListEmptyDataIsArray(t *testing.T) {
	response, err := hubThreadList(context.Background(), hubcore.WebConfig{}, appsource.NewRegistry(), appwire.ThreadListParams{})
	if err != nil {
		t.Fatalf("hubThreadList: %v", err)
	}
	if response.Data == nil || len(response.Data) != 0 {
		t.Fatalf("thread list data = %#v, want non-nil empty slice", response.Data)
	}
	wire, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal thread list: %v", err)
	}
	var decoded struct {
		Data []appwire.Thread `json:"data"`
	}
	if err := json.Unmarshal(wire, &decoded); err != nil {
		t.Fatalf("unmarshal thread list: %v", err)
	}
	if decoded.Data == nil || len(decoded.Data) != 0 {
		t.Fatalf("serialized thread list data = %#v, want empty JSON array", decoded.Data)
	}
}
