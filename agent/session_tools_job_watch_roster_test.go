package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"primeradiant.com/evener/appwire"
)

func TestHubRosterWatchSchema(t *testing.T) {
	t.Parallel()
	_, wire := managerProviderSchemas(t)
	encoded, err := json.Marshal(wire["job_watch"])
	if err != nil {
		t.Fatal(err)
	}
	schema, err := jsonschema.CompileString("https://manager.test/roster-job-watch.json", string(encoded))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		hub   map[string]any
		valid bool
	}{
		{"scope", map[string]any{"scope": "top_level"}, true},
		{"scope and ref", map[string]any{"scope": "top_level", "ref": "local:worker"}, false},
		{"scope and completion", map[string]any{"scope": "top_level", "completion": true}, false},
		{"scope and events", map[string]any{"scope": "top_level", "events": []any{"status"}}, false},
		{"scope and empty events", map[string]any{"scope": "top_level", "events": []any{}}, false},
		{"scope and false completion", map[string]any{"scope": "top_level", "completion": false}, false},
		{"unknown scope", map[string]any{"scope": "all"}, false},
		{"target", map[string]any{"ref": "local:worker", "events": []any{"status"}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			args := map[string]any{"operation": "create", "hub": tc.hub, "intent": "checking roster"}
			if err := schema.Validate(args); (err == nil) != tc.valid {
				t.Errorf("valid=%v want=%v: %v", err == nil, tc.valid, err)
			}
		})
	}
}

func TestHubRosterWatchRuntimeSelectors(t *testing.T) {
	t.Parallel()
	for _, selectors := range []map[string]any{
		{"scope": "top_level", "events": []any{}},
		{"scope": "top_level", "completion": false},
		{"scope": "top_level", "ref": ""},
	} {
		_, err := (&Session{}).configureHubWatchRuntime(context.Background(), map[string]any{"operation": "create", "hub": selectors})
		var wire appwire.WireError
		if !errors.As(err, &wire) || wire.Code != appwire.CodeInvalidParams {
			t.Fatalf("runtime scope selectors=%+v: got %v, want typed invalid params", selectors, err)
		}
	}
}
