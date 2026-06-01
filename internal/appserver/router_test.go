package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/serf/appwire"
)

func TestRouterDispatchesTypedHandler(t *testing.T) {
	router := NewRouter()
	HandleTyped(router, appwire.MethodThreadList, func(_ context.Context, params appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		if params.Limit != 3 {
			t.Fatalf("limit=%d, want 3", params.Limit)
		}
		return appwire.ThreadListResponse{Data: []appwire.Thread{{ID: "th_1"}}}, nil
	})
	raw, _ := json.Marshal(appwire.ThreadListParams{Limit: 3})
	resp, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodThreadList,
		Params: raw,
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	list, ok := resp.(appwire.ThreadListResponse)
	if !ok {
		t.Fatalf("response type=%T", resp)
	}
	if len(list.Data) != 1 || list.Data[0].ID != "th_1" {
		t.Fatalf("list=%+v", list)
	}
}
