package hub

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubProjectDeleteAppWireRejectsMissingKey(t *testing.T) {
	web := NewWebServer(hubcore.WebConfig{
		Past:   hubcore.NewPastIndex(""),
		Roster: hubcore.NewRosterWithEntries(),
	})

	_, err := dispatchProjectDelete(t, web, appwire.ProjectDeleteParams{})
	var wireErr appwire.WireError
	if !errors.As(err, &wireErr) {
		t.Fatalf("error = %v, want AppWire error", err)
	}
	if wireErr.Code != appwire.CodeInvalidParams {
		t.Fatalf("error code = %d, want %d (%v)", wireErr.Code, appwire.CodeInvalidParams, wireErr)
	}
	if wireErr.Message != "key and workingDir are required" {
		t.Fatalf("error message = %q", wireErr.Message)
	}
}

func dispatchProjectDelete(t *testing.T, web *WebServer, params appwire.ProjectDeleteParams) (appwire.ProjectDeleteResponse, error) {
	t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal project delete params: %v", err)
	}
	result, err := web.appRPC.Router().Dispatch(context.Background(), appwire.Request{
		ID:     appwire.NewIntID(1),
		Method: appwire.MethodEvenerProjectDelete,
		Params: raw,
	})
	if err != nil {
		return appwire.ProjectDeleteResponse{}, err
	}
	response, ok := result.(appwire.ProjectDeleteResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.ProjectDeleteResponse", result)
	}
	return response, nil
}
