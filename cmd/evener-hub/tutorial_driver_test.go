package hub

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTutorialDriverActionData(t *testing.T) {
	var requests bytes.Buffer
	driver := tutorialDriver{
		encode: json.NewEncoder(&requests),
		decode: json.NewDecoder(strings.NewReader(`{"id":1,"ok":true,"data":{}}
{"id":2,"ok":true,"data":{"events":[{"kind":"edit","trusted":true,"visible":true}]}}`)),
	}
	driver.act(t, "open", nil) // Callers that need no data still advance the protocol.
	data := driver.act(t, "configure", map[string]int{"value": 7})
	var result struct {
		Events []struct {
			Kind             string
			Trusted, Visible bool
		}
	}
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Events) != 1 || result.Events[0].Kind != "edit" || !result.Events[0].Trusted || !result.Events[0].Visible {
		t.Fatal("driver lost structured browser event data")
	}
	decoder := json.NewDecoder(&requests)
	for i, action := range []string{"open", "configure"} {
		var request struct {
			ID     int
			Action string
			Args   map[string]int
		}
		if err := decoder.Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ID != i+1 || request.Action != action {
			t.Fatal("driver request protocol differs")
		}
		if action == "configure" && request.Args["value"] != 7 {
			t.Fatal("driver lost configuration arguments")
		}
	}
}
