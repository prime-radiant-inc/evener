package llm

import (
	"reflect"
	"testing"
	"time"
)

func TestAPIAttemptRecordCarriesPrunedFields(t *testing.T) {
	meta := APIAttemptMeta{ProviderInstance: "groq", RequestModel: "m", StartedAt: time.Now(), PrunedFields: []string{"store", "stream_options"}}
	record := buildAPIAttemptRecord("ag_1", "at_1", 0, meta, APIAttemptResult{StatusCode: 200, FinishedAt: time.Now()})
	if !reflect.DeepEqual(record.Request.PrunedFields, []string{"store", "stream_options"}) {
		t.Fatalf("pruned fields = %v", record.Request.PrunedFields)
	}
	empty := buildAPIAttemptRecord("ag_1", "at_2", 1, APIAttemptMeta{StartedAt: time.Now()}, APIAttemptResult{FinishedAt: time.Now()})
	if empty.Request.PrunedFields != nil {
		t.Fatalf("expected no pruned fields, got %v", empty.Request.PrunedFields)
	}
}
