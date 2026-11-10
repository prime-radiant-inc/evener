package appserver

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
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

func TestRouterMethodsListsRegisteredHandlers(t *testing.T) {
	if methods := NewRouter().Methods(); len(methods) != 0 {
		t.Fatalf("Methods on an empty router = %v, want none", methods)
	}
	router := NewRouter()
	HandleTyped(router, appwire.MethodThreadList, func(_ context.Context, _ appwire.ThreadListParams) (appwire.ThreadListResponse, error) {
		return appwire.ThreadListResponse{}, nil
	})
	HandleTyped(router, appwire.MethodInitialize, func(_ context.Context, _ appwire.InitializeParams) (appwire.InitializeResponse, error) {
		return appwire.InitializeResponse{}, nil
	})
	methods := router.Methods()
	slices.Sort(methods)
	want := []string{appwire.MethodInitialize, appwire.MethodThreadList}
	slices.Sort(want)
	if !slices.Equal(methods, want) {
		t.Fatalf("Methods = %v, want %v", methods, want)
	}
}

func TestRouterDispatchMethodNotFound(t *testing.T) {
	router := NewRouter()
	_, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: "nonexistent/method",
	})
	if wire, ok := errors.AsType[appwire.WireError](err); !ok || wire.Code != appwire.CodeMethodNotFound {
		t.Fatalf("Dispatch of an unknown method err = %v, want a method-not-found wire error", err)
	}
}

// A handler with nothing to return still answers: Dispatch turns its nil
// result into an empty response.
func TestRouterDispatchNilResponse(t *testing.T) {
	router := NewRouter()
	router.Handle("test/null", func(_ context.Context, _ json.RawMessage) (any, error) {
		return nil, nil
	})
	resp, err := router.Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: "test/null",
	})
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if _, ok := resp.(appwire.EmptyResponse); !ok {
		t.Fatalf("response type = %T, want EmptyResponse", resp)
	}
}

func TestWireErrorWithGenericError(t *testing.T) {
	result := WireError(errors.New("something went wrong"))
	if result.Code != appwire.CodeInternalError {
		t.Fatalf("WireError code = %d, want %d", result.Code, appwire.CodeInternalError)
	}
}
