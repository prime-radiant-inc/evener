package appwire

import (
	"encoding/json"
	"testing"
)

func TestGitHeadResponseUsesHeadWireField(t *testing.T) {
	got, err := json.Marshal(GitHeadResponse{Head: "main"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"head":"main"}` {
		t.Fatalf("GitHeadResponse JSON = %s, want head field", got)
	}
}

func TestGitHeadMethodIsHubScoped(t *testing.T) {
	for _, method := range Methods {
		if method.Name != MethodEvenerGitHead {
			continue
		}
		if method.Scope != ScopeHub {
			t.Fatalf("git head scope=%q, want %q", method.Scope, ScopeHub)
		}
		if _, ok := method.Params.(GitHeadParams); !ok {
			t.Fatalf("git head params=%T, want GitHeadParams", method.Params)
		}
		if _, ok := method.Result.(GitHeadResponse); !ok {
			t.Fatalf("git head result=%T, want GitHeadResponse", method.Result)
		}
		return
	}
	t.Fatalf("method catalog missing %q", MethodEvenerGitHead)
}
