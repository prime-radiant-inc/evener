package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
)

func TestSummaryFailureWarningKeepsRouteInCLI(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join("..", "..", "agent", "testdata", "summaryfailurewire", "events.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Warning events.WarningData `json:"warning"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	channel := make(chan events.SessionEvent, 1)
	channel <- events.New(fixture.Warning)
	close(channel)
	var output bytes.Buffer
	<-drainEventsHuman(channel, &output)
	for _, identifier := range []string{"summary_failure", "openai/summary-model-8af1", "429", "rate_limit"} {
		if !strings.Contains(output.String(), identifier) {
			t.Errorf("printed warning lost %s", identifier)
		}
	}
	if !strings.Contains(output.String(), fixture.Warning.Message) {
		t.Error("CLI did not print the producer message")
	}
}
