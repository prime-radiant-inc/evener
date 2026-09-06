package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubThreadPagingHandlersValidateItemLimits(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{}, appsource.NewRegistry())
	cases := []struct {
		name   string
		method string
		params any
	}{
		{name: "read item limit above maximum", method: appwire.MethodThreadRead, params: appwire.ThreadReadParams{ItemLimit: appwire.TranscriptItemPageLimit + 1}},
		{name: "list item limit above maximum", method: appwire.MethodThreadTurnsList, params: appwire.ThreadTurnsListParams{Cursor: "opaque", ItemLimit: appwire.TranscriptItemPageLimit + 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.Router().Dispatch(context.Background(), appwire.Request{
				ID: appwire.NewIntID(1), Method: tc.method, Params: mustPagingJSON(t, tc.params),
			})
			var wireErr appwire.WireError
			if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams {
				t.Fatalf("error = %T %v, want invalid params", err, err)
			}
		})
	}
}
