package appserver

import (
	"context"
	"encoding/json"
	"testing"

	"primeradiant.com/evener/appwire"
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

// TestRouterAdmissionNilReleaseStillDispatches proves Dispatch tolerates an
// admission that holds no resource: a nil release with a nil error means
// "admitted, nothing to release", not "admission never ran". Dispatch must run
// the handler and return its result rather than dereferencing the nil release
// when the handler returns.
func TestRouterAdmissionNilReleaseStillDispatches(t *testing.T) {
	router := NewRouter()
	router.SetAdmission(func(context.Context, string) (func(), error) { return nil, nil })
	var called bool
	router.Handle(appwire.MethodThreadList, func(context.Context, json.RawMessage) (any, error) {
		called = true
		return appwire.ThreadListResponse{}, nil
	})
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("Dispatch panicked on a nil admission release: %v", recovered)
		}
	}()
	out, err := router.Dispatch(context.Background(), appwire.Request{Method: appwire.MethodThreadList})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if !called {
		t.Fatal("handler was not invoked")
	}
	if _, ok := out.(appwire.ThreadListResponse); !ok {
		t.Fatalf("response type=%T", out)
	}
}
