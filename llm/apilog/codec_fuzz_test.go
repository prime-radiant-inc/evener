package apilog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"primeradiant.com/serf/identifier"
)

func FuzzCanonicalRecordCodec(f *testing.F) {
	f.Add([]byte("request"), []byte("data: {}\n\n"), uint8(1))
	f.Add([]byte{0, 1, 2, 255}, []byte{255, 0, 10}, uint8(2))
	f.Fuzz(func(t *testing.T, requestBody, responseBody []byte, attempts uint8) {
		if len(requestBody) > 4096 {
			requestBody = requestBody[:4096]
		}
		if len(responseBody) > 4096 {
			responseBody = responseBody[:4096]
		}
		count := int(attempts%8) + 1
		attemptID := identifier.MustNewAPIAttemptID()
		attempt := APIAttemptRecord{
			Kind:             "api_attempt",
			SchemaVersion:    1,
			AttemptID:        attemptID,
			AttemptGroupID:   "ag_codec_fuzz",
			AttemptIndex:     count,
			Timestamp:        time.Unix(1, int64(count)).UTC(),
			LatencyMS:        int64(count),
			ProviderInstance: "scripted",
			RequestModel:     "model",
			Request: APIAttemptRequest{
				Method:   "POST",
				Endpoint: "https://provider.invalid/v1/responses",
				Body:     EncodeBody(requestBody),
			},
			Response: &APIAttemptResponse{
				StatusCode:    recordTestInt(200),
				Body:          EncodeBody(responseBody),
				TextLength:    recordTestInt(len(responseBody)),
				ToolCallCount: recordTestInt(count % 3),
			},
			Outcome: AttemptSuccess,
		}
		settlement := APIAttemptGroupSettlement{
			Kind:              "attempt_group_settlement",
			SchemaVersion:     1,
			AttemptGroupID:    attempt.AttemptGroupID,
			FinalAttemptID:    attemptID,
			FinalAttemptCount: count,
			Outcome:           AttemptSuccess,
			SettledAt:         attempt.Timestamp.Add(time.Second),
		}

		attemptLine, err := json.Marshal(attempt)
		if err != nil {
			t.Fatal(err)
		}
		settlementLine, err := json.Marshal(settlement)
		if err != nil {
			t.Fatal(err)
		}
		stream := append(append(append([]byte(nil), attemptLine...), '\n'), settlementLine...)
		stream = append(stream, '\n')
		decoder := NewDecoder(bytes.NewReader(stream), 1<<20)
		record, err := decoder.Next()
		if err != nil {
			t.Fatal(err)
		}
		gotAttempt := record.(APIAttemptRecord)
		gotRequest, err := DecodeBody(gotAttempt.Request.Body)
		if err != nil || !bytes.Equal(gotRequest, requestBody) {
			t.Fatalf("request body mismatch: %v", err)
		}
		gotResponse, err := DecodeBody(gotAttempt.Response.Body)
		if err != nil || !bytes.Equal(gotResponse, responseBody) {
			t.Fatalf("response body mismatch: %v", err)
		}
		if record, err := decoder.Next(); err != nil {
			t.Fatal(err)
		} else if got := record.(APIAttemptGroupSettlement); got.FinalAttemptCount != count || got.FinalAttemptID != attemptID {
			t.Fatalf("settlement = %+v", got)
		}
		if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
			t.Fatalf("terminal decode error = %v", err)
		}

		partial := append(append(append([]byte(nil), attemptLine...), '\n'), settlementLine[:len(settlementLine)/2]...)
		decoder = NewDecoder(bytes.NewReader(partial), 1<<20)
		if _, err := decoder.Next(); err != nil {
			t.Fatal(err)
		}
		if _, err := decoder.Next(); !errors.Is(err, ErrPartialTail) {
			t.Fatalf("partial-tail error = %v", err)
		}
	})
}
