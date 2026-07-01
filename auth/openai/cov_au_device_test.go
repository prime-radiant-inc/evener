package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestFlexibleNumberUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    flexibleNumber
		wantErr bool
	}{
		{"json null", `null`, 0, false},
		{"quoted number", `"7"`, 7, false},
		{"bare number", `9`, 9, false},
		{"empty quoted string", `""`, 0, false},
		{"quoted whitespace", `"  "`, 0, false},
		{"quoted non-numeric", `"abc"`, 0, true},
		{"bare non-numeric", `abc`, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var n flexibleNumber
			err := json.Unmarshal([]byte(tt.input), &n)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("UnmarshalJSON(%s) error = nil, want error", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("UnmarshalJSON(%s) error = %v", tt.input, err)
			}
			if n != tt.want {
				t.Fatalf("UnmarshalJSON(%s) = %d, want %d", tt.input, n, tt.want)
			}
		})
	}
}

func TestRequestDeviceCodeCreatesClientWhenNil(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"device_auth_id": "dev-nil",
			"user_code":      "NILC",
			"interval":       "5",
		})
	}

	dc, err := RequestDeviceCode(context.Background(), nil, m.cfg())
	if err != nil {
		t.Fatalf("RequestDeviceCode(nil client) error = %v", err)
	}
	if dc.DeviceAuthID != "dev-nil" {
		t.Fatalf("DeviceAuthID = %q, want dev-nil", dc.DeviceAuthID)
	}
}

func TestRequestDeviceCodeRejectsInvalidJSON(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not json"))
	}

	_, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err == nil || !strings.Contains(err.Error(), "decode device usercode response") {
		t.Fatalf("RequestDeviceCode() error = %v, want decode failure", err)
	}
}

func TestRequestDeviceCodeRejectsMissingFields(t *testing.T) {
	m := newDeviceMockServer(t)
	m.usercode = func(w http.ResponseWriter, r *http.Request) {
		// device_auth_id present but user_code missing.
		writeJSON(t, w, http.StatusOK, map[string]any{"device_auth_id": "dev-x"})
	}

	_, err := RequestDeviceCode(context.Background(), m.server.Client(), m.cfg())
	if err == nil || !strings.Contains(err.Error(), "missing device_auth_id or user_code") {
		t.Fatalf("RequestDeviceCode() error = %v, want missing-fields error", err)
	}
}

func TestPollDeviceAuthOnceCreatesClientWhenNil(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, http.StatusOK, map[string]any{
			"authorization_code": "auth-nil", "code_challenge": "chal", "code_verifier": "ver-nil",
		})
	}

	got, pending, err := PollDeviceAuthOnce(context.Background(), nil, m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err != nil || pending {
		t.Fatalf("PollDeviceAuthOnce(nil client) pending=%v err=%v", pending, err)
	}
	if got.AuthorizationCode != "auth-nil" {
		t.Fatalf("AuthorizationCode = %q, want auth-nil", got.AuthorizationCode)
	}
}

func TestPollDeviceAuthOnceRejectsMissingFields(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		// 200 but no authorization_code / code_verifier.
		writeJSON(t, w, http.StatusOK, map[string]any{"code_challenge": "chal"})
	}

	_, _, err := PollDeviceAuthOnce(context.Background(), m.server.Client(), m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err == nil || !strings.Contains(err.Error(), "missing authorization_code or code_verifier") {
		t.Fatalf("PollDeviceAuthOnce() error = %v, want missing-fields error", err)
	}
}

func TestPollDeviceAuthOnceRejectsInvalidJSON(t *testing.T) {
	m := newDeviceMockServer(t)
	m.token = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{not json"))
	}

	_, _, err := PollDeviceAuthOnce(context.Background(), m.server.Client(), m.cfg(), DeviceCode{DeviceAuthID: "d", UserCode: "C"})
	if err == nil || !strings.Contains(err.Error(), "decode device poll response") {
		t.Fatalf("PollDeviceAuthOnce() error = %v, want decode failure", err)
	}
}

func TestCtxSleepZeroDuration(t *testing.T) {
	// A non-positive duration returns immediately when the context is live.
	if err := ctxSleep(context.Background(), 0); err != nil {
		t.Fatalf("ctxSleep(live, 0) = %v, want nil", err)
	}

	// A non-positive duration still reports a cancelled context.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ctxSleep(ctx, -time.Second); err == nil {
		t.Fatal("ctxSleep(cancelled, -1s) = nil, want context error")
	}
}
