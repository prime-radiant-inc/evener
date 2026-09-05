package hub

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/appsource"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
)

func TestHubThreadPagingHandlersValidateRequests(t *testing.T) {
	server := newHubAppServer(hubcore.WebConfig{}, appsource.NewRegistry())
	cases := []struct {
		name   string
		method string
		params any
		want   string
	}{
		{
			name:   "read item limit outside item mode",
			method: appwire.MethodThreadRead,
			params: appwire.ThreadReadParams{ItemLimit: 1},
			want:   "itemLimit requires pageUnit \"item\"",
		},
		{
			name:   "list item limit outside item mode",
			method: appwire.MethodThreadTurnsList,
			params: appwire.ThreadTurnsListParams{ItemLimit: 1},
			want:   "itemLimit requires pageUnit \"item\"",
		},
		{
			name:   "read item mode with turn limit",
			method: appwire.MethodThreadRead,
			params: appwire.ThreadReadParams{PageUnit: appwire.TranscriptPageUnitItem, TurnLimit: 1},
			want:   "turnLimit and itemLimit cannot be supplied together",
		},
		{
			name:   "list item mode with turn limit",
			method: appwire.MethodThreadTurnsList,
			params: appwire.ThreadTurnsListParams{PageUnit: appwire.TranscriptPageUnitItem, Limit: 1},
			want:   "limit and itemLimit cannot be supplied together",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := server.Router().Dispatch(context.Background(), appwire.Request{
				ID: appwire.NewIntID(1), Method: tc.method, Params: mustJSON(t, tc.params),
			})
			var wireErr appwire.WireError
			if !errors.As(err, &wireErr) || wireErr.Code != appwire.CodeInvalidParams || wireErr.Message != tc.want {
				t.Fatalf("error = %T %v, want invalid params %q", err, err, tc.want)
			}
		})
	}
}
