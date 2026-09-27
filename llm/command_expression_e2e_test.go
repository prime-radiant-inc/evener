package llm_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// The end-to-end shape the command-expression feature exists for: a gateway
// whose credential is minted by a command, through the real resolve and apply
// path, with the scripted server receiving the minted bearer — and the token
// never appearing in the API log that records the wire exchange.
func TestCommandExpressionMintsBearerEndToEnd(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "e2e-minted-token", nil
	}

	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		gotAuth = request.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"gpt-test"}]}`)
	}))
	t.Cleanup(server.Close)

	client := llm.NewClient(llm.WithRegistry(fixtureRegistry(t, server.URL, map[string]registry.Provider{
		"command-expression-gateway": {
			Base: "openai", APIKey: `$(get-gateway-token)`,
			Transport: registry.Transport{BaseURL: server.URL},
		},
	})))
	logger, logPath := attachProviderOperationLogger(t, client)
	t.Cleanup(func() { _ = logger.Close() })

	if _, err := client.Models(context.Background(), "command-expression-gateway"); err != nil {
		t.Fatalf("model list: %v", err)
	}
	if gotAuth != "Bearer e2e-minted-token" {
		t.Fatalf("server saw Authorization = %q; want the minted bearer", gotAuth)
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times for one request; want 1", runs)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "e2e-minted-token") {
		t.Fatal("the minted token appears in the API log")
	}
	// The log did record the request, so the absence above is redaction, not
	// a missing record.
	if !strings.Contains(string(data), "command-expression-gateway") {
		t.Fatal("the API log has no record of the gateway request")
	}
}
