package google

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func FuzzGoogleComplete(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"id":"int_1","model":"gemini-3.8-flash","status":"completed","steps":[{"type":"model_output","content":[{"type":"text","text":"hi"}]}],"usage":{"total_input_tokens":3,"total_output_tokens":1,"total_tokens":4}}`),
		[]byte(`{"id":"int_2","status":"requires_action","steps":[{"type":"function_call","id":"c1","name":"shell","arguments":{"cmd":"ls"},"signature":"sig1"}]}`),
		[]byte(`{"id":"int_3","status":"failed","errors":[{"code":"resource_exhausted","message":"resource exhausted"}]}`),
		[]byte(`{"id":"int_4","status":"failed","errors":[{"code":"safety","message":"blocked"}]}`),
		[]byte(`{"id":"int_5","status":"queued"}`),
		[]byte(`{"id":"int_6","status":"incomplete","steps":[{"type":"model_output","content":[{"type":"text","text":"part"}]}],"continuation_token":"ct1"}`),
		[]byte(`{"steps":"not-an-array"}`),
		[]byte(`{}`),
		[]byte(``),
		[]byte(`not json`),
		[]byte("{\"steps\":\xff}"),
		[]byte(`[1,2,3]`),
	}
	for _, s := range seeds {
		f.Add(byte(200), s)
	}
	f.Add(byte(29), []byte(`{"error":{"message":"quota"}}`))
	f.Add(byte(200), []byte(`{"error":{"code":400,"message":"deadline","status":"DEADLINE_EXCEEDED"}}`))
	f.Add(byte(200), []byte(`{"error":{"code":400,"message":"unavailable","status":"UNAVAILABLE"}}`))
	f.Add(byte(200), []byte(`{"id":"int_7","status":"completed","steps":[{"type":"google_search_call","id":"gs1","arguments":{"queries":["q1",42,"q2"]}},{"type":"google_search_result","call_id":"gs1","result":[{"url":"https://a","search_suggestions":"<div></div>"}]}]}`))

	f.Fuzz(func(t *testing.T, statusSel byte, body []byte) {
		status := http.StatusOK
		if statusSel != 0 {
			status = 200 + int(statusSel)%300
		}

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)

		resp, err := (&Protocol{Client: srv.Client()}).Complete(context.Background(), protoReq(""), protoLive(srv))

		if status < 200 || status >= 300 {
			if err == nil {
				t.Fatalf("Complete: nil error for HTTP status %d (body %q)", status, body)
			}
			return
		}
		if err != nil {
			return
		}
		if resp.Provider != "gemini-prod" {
			t.Fatalf("Complete on 2xx: provider = %q, want the instance name (body %q)", resp.Provider, body)
		}
	})
}
