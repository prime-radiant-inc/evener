package appwire

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

func TestClientGitHeadRoundTrip(t *testing.T) {
	written := roundTrip(t, MethodEvenerGitHead, GitHeadResponse{Head: "main"}, func(ctx context.Context, client *Client) error {
		resp, err := client.GitHead(ctx, GitHeadParams{CWD: "/repo"})
		if err != nil {
			return err
		}
		if resp.Head != "main" {
			return fmt.Errorf("head=%q, want main", resp.Head)
		}
		return nil
	})
	assertWrittenParams(t, "GitHead", written.Request.Params, `{"cwd":"/repo"}`)
}

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
