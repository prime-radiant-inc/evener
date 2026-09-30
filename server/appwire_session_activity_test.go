package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestSessionActivityPublicRoutes(t *testing.T) {
	for _, method := range []string{
		"evener/thread/activity/read", "evener/thread/delegates/list",
		"evener/thread/jobs/list", "evener/thread/watches/list",
	} {
		t.Run(method, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetAppIdentity("local", "root")
			_, err := s.AppServer().Router().Dispatch(context.Background(), appwire.Request{
				Method: method, Params: json.RawMessage(`{"ref":"local:root"}`),
			})
			var wire appwire.WireError
			if !errors.As(err, &wire) || wire.Code != appwire.CodeUnavailable {
				t.Fatalf("registered read without a session hook = %v; want unavailable", err)
			}
		})
	}
}
