package integrations_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestInitializeResponse(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("initialize-result.json")
	if err != nil {
		t.Fatal(err)
	}
	var result appwire.InitializeResponse
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.ProtocolVersion != appwire.ProtocolVersion || result.SourceID == "" || !result.Features.ThreadList {
		t.Fatalf("invalid initialization result: %+v", result)
	}
}

func TestHandshakeFrames(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("handshake.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	scan := bufio.NewScanner(bytes.NewReader(data))
	count := 0
	for scan.Scan() {
		var message appwire.Message
		if err := json.Unmarshal(scan.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		var frame struct {
			ID     *int            `json:"id,omitempty"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		dec := json.NewDecoder(bytes.NewReader(scan.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&frame); err != nil {
			t.Fatal(err)
		}
		var params any
		switch count {
		case 0:
			if frame.Method != appwire.MethodInitialize || frame.ID == nil || *frame.ID != 1 {
				t.Fatalf("invalid initialization: %+v", frame)
			}
			params = &appwire.InitializeParams{}
		case 1:
			if frame.Method != "initialized" || frame.ID != nil {
				t.Fatalf("invalid notification: %+v", frame)
			}
			params = &struct{}{}
		case 2:
			if frame.Method != appwire.MethodThreadList || frame.ID == nil || *frame.ID != 2 {
				t.Fatalf("invalid listing: %+v", frame)
			}
			params = &appwire.ThreadListParams{}
		default:
			t.Fatal("unexpected frame")
		}
		dec = json.NewDecoder(bytes.NewReader(frame.Params))
		dec.DisallowUnknownFields()
		if err := dec.Decode(params); err != nil {
			t.Fatal(err)
		}
		if init, ok := params.(*appwire.InitializeParams); ok && (init.ProtocolVersion != appwire.ProtocolVersion || init.ClientInfo.Name == "") {
			t.Fatalf("invalid handshake parameters: %+v", init)
		}
		if list, ok := params.(*appwire.ThreadListParams); ok && list.Limit != 20 {
			t.Fatalf("unbounded listing: %+v", list)
		}
		count++
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("frames = %d, want 3", count)
	}
}
