package agent

import (
	"encoding/json"
	"testing"
)

func TestAPILogSummarySizerMatchesTrimmedEnvelope(t *testing.T) {
	envelope := apiLogReadEnvelope{TranscriptRef: "session", Source: "api-log", Records: []apiLogRecordSummary{
		{RecordNumber: 1, Kind: "request", RequestModel: "model"},
		{RecordNumber: 2, Kind: "response", ErrorClass: "multi\nline"},
		{RecordNumber: 3, Kind: "request"},
	}}
	full, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	sizer, err := newAPILogSummaryEnvelopeSizer(envelope, len(full))
	if err != nil {
		t.Fatal(err)
	}
	for {
		envelope.Meta.RecordsReturned = len(envelope.Records)
		envelope.Meta.Truncated = len(envelope.Records) < 3
		actual, err := json.MarshalIndent(envelope, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		size, err := sizer.envelopeSize(envelope)
		if err != nil {
			t.Fatal(err)
		}
		if size != len(actual) {
			t.Fatalf("%d records: predicted=%d actual=%d", len(envelope.Records), size, len(actual))
		}
		if len(envelope.Records) == 0 {
			break
		}
		envelope.Records = envelope.Records[1:]
	}
}
