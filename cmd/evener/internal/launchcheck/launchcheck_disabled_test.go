package launchcheck

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestLaunchCheckModelsOmitDisabled(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"gpt-live"}]}`, "[providers.gw.models.\"gpt-live\"]\ndisabled = true\n")

	var stdout, stderr bytes.Buffer
	if err := RunLaunchCheck([]string{"--protocol", appwire.ProtocolVersion, "--models", "--json"}, &stdout, &stderr); err != nil {
		t.Fatalf("runLaunchCheck: %v stderr=%s", err, stderr.String())
	}
	var out struct {
		Models []struct {
			Provider string `json:"provider"`
			Model    string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode stdout %q: %v", stdout.String(), err)
	}
	for _, m := range out.Models {
		if m.Provider == "gw" && m.Model == "gpt-live" {
			t.Fatalf("disabled model listed: %+v", out.Models)
		}
	}
}

func TestLaunchCheckRejectsDisabledModel(t *testing.T) {
	launchCheckGateway(t, http.StatusOK, `{"data":[{"id":"gpt-live"}]}`, "[providers.gw.models.\"gpt-live\"]\ndisabled = true\n")

	var stdout, stderr bytes.Buffer
	err := RunLaunchCheck([]string{"--protocol", appwire.ProtocolVersion, "--model", "gw/gpt-live", "--json"}, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected disabled model rejection")
	}
	if !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("error=%v, want it to name the disablement", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout=%q, want empty on failure", stdout.String())
	}
}
