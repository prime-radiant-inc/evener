package llm

import (
	"errors"
	"testing"

	"primeradiant.com/evener/llm/registry"
)

func FuzzClassifyHTTPError(f *testing.F) {
	f.Add(413, []byte(`{"error":{"code":"rate_limit_exceeded"}}`))
	f.Add(400, []byte(`{"error":{"message":"Unknown parameter: 'store'.","code":"unknown_parameter","param":"store"}}`))
	f.Add(429, []byte(`{"error":{"type":"usage_limit_reached","resets_in_seconds":10}}`))
	f.Add(400, []byte(`{"error":"a string, not an object"}`))
	f.Add(503, []byte(`not json`))
	res := registry.Resolved{Instance: "inst", Protocol: registry.ProtocolOpenAIChat, ModelID: "m"}
	f.Fuzz(func(t *testing.T, status int, body []byte) {
		err := ClassifyHTTPError("op", status, nil, body, res)
		var le Error
		if !errors.As(err, &le) {
			t.Fatalf("not an llm.Error: %v", err)
		}
		if le.StatusCode() != status || le.Provider() != "inst" || ErrorProtocol(err) != registry.ProtocolOpenAIChat {
			t.Fatalf("stamps lost: %v", err)
		}
		_ = err.Error()
	})
}
