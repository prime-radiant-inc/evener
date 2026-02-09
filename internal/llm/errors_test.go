package llm

import (
	"testing"
	"time"
)

func TestParseRetryAfter_Seconds(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("12", now)
	if d == nil || *d != 12*time.Second {
		t.Fatalf("got %v want 12s", d)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 2, 7, 0, 0, 0, 0, time.UTC)
	d := ParseRetryAfter("Sat, 07 Feb 2026 00:00:10 GMT", now)
	if d == nil || *d != 10*time.Second {
		t.Fatalf("got %v want 10s", d)
	}
}

func TestErrorFromHTTPStatus_MappingAndRetryable(t *testing.T) {
	cases := []struct {
		status    int
		wantType  any
		retryable bool
	}{
		{status: 400, wantType: &InvalidRequestError{}, retryable: false},
		{status: 401, wantType: &AuthenticationError{}, retryable: false},
		{status: 403, wantType: &AccessDeniedError{}, retryable: false},
		{status: 404, wantType: &NotFoundError{}, retryable: false},
		{status: 408, wantType: &RequestTimeoutError{}, retryable: true},
		{status: 413, wantType: &ContextLengthError{}, retryable: false},
		{status: 422, wantType: &InvalidRequestError{}, retryable: false},
		{status: 429, wantType: &RateLimitError{}, retryable: true},
		{status: 500, wantType: &ServerError{}, retryable: true},
		{status: 503, wantType: &ServerError{}, retryable: true},
		{status: 599, wantType: &UnknownHTTPError{}, retryable: true},
	}
	for _, tc := range cases {
		err := ErrorFromHTTPStatus("p", tc.status, "msg", nil, nil)
		switch tc.wantType.(type) {
		case *InvalidRequestError:
			if _, ok := err.(*InvalidRequestError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AuthenticationError:
			if _, ok := err.(*AuthenticationError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *AccessDeniedError:
			if _, ok := err.(*AccessDeniedError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *NotFoundError:
			if _, ok := err.(*NotFoundError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RequestTimeoutError:
			if _, ok := err.(*RequestTimeoutError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ContextLengthError:
			if _, ok := err.(*ContextLengthError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *RateLimitError:
			if _, ok := err.(*RateLimitError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *ServerError:
			if _, ok := err.(*ServerError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		case *UnknownHTTPError:
			if _, ok := err.(*UnknownHTTPError); !ok {
				t.Fatalf("status %d: got %T", tc.status, err)
			}
		}
		e, ok := err.(Error)
		if !ok {
			t.Fatalf("status %d: not an llm.Error (%T)", tc.status, err)
		}
		if e.Retryable() != tc.retryable {
			t.Fatalf("status %d: retryable=%t want %t", tc.status, e.Retryable(), tc.retryable)
		}
	}
}

