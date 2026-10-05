package appwire

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestJobsOutputExplicitZero(t *testing.T) {
	t.Parallel()
	var params JobsOutputParams
	if err := json.Unmarshal([]byte(`{"ref":"owner","jobId":"job","beforeBytes":0}`), &params); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["beforeBytes"]) != "0" {
		t.Fatalf("explicit zero omitted: %s", data)
	}
	// Decode omission into a fresh value, independent of the request builder.
	var omitted JobsOutputParams
	if err := json.Unmarshal([]byte(`{"jobId":"job"}`), &omitted); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(omitted)
	if err != nil {
		t.Fatal(err)
	}
	fields = nil
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["beforeBytes"]; present {
		t.Fatalf("omitted selector was serialized: %s", data)
	}
}

func TestJobsOutputRawPageJSON(t *testing.T) {
	t.Parallel()
	const input = `{"data":{"offsetBytes":3,"bytesReturned":3,"totalBytes":6,"retainedStartBytes":0,"encoding":"base64","data":"qWNk"}}`
	var response JobsOutputResponse
	if err := json.Unmarshal([]byte(input), &response); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(input), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("raw page round trip = %s, want %s", encoded, input)
	}
}

func TestJobOutputProtocolVersion(t *testing.T) {
	t.Parallel()
	if ProtocolVersion != "evener-appwire-v7" {
		t.Fatalf("protocol = %q, want coordinated page version evener-appwire-v7", ProtocolVersion)
	}
}
