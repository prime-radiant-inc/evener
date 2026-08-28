package hub

import (
	"context"
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
	result, err := exactDispatch(context.Background(), t, web.appRPC, appwire.MethodEvenerProjectDelete, params)
	if err != nil {
		return appwire.ProjectDeleteResponse{}, err
	}
	response, ok := result.(appwire.ProjectDeleteResponse)
	if !ok {
		t.Fatalf("response type = %T, want appwire.ProjectDeleteResponse", result)
	}
	return response, nil
}
