package llm

import (
	"errors"
	"fmt"
	"strconv"
	"testing"
)

func TestImageNormalizationRecognition(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"rejection", 400, `{"error":{"message":"Image normalization failed"}}`, true},
		{"mixed case", 422, `{"error":{"message":"IMAGE Normalization FAILED"}}`, true},
		{"plain body", 400, `Image normalization failed`, true},
		{"server failure", 500, `{"error":{"message":"Image normalization failed"}}`, false},
		{"payload size", 413, `{"error":{"message":"Image normalization failed"}}`, false},
		{"unrelated", 400, `{"error":{"message":"Unknown parameter: temperature"}}`, false},
		{"context code", 400, `{"error":{"message":"Image normalization failed","code":"context_length_exceeded"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ClassifyHTTPError("chat.completions", tc.status, nil, []byte(tc.body), chatRes(""))
			if got := IsImageNormalizationError(fmt.Errorf("wrapped: %w", err)); got != tc.want {
				t.Fatalf("image normalization = %v, want %v, error %v", got, tc.want, err)
			}
		})
	}
	if IsImageNormalizationError(nil) || IsImageNormalizationError(errors.New("Image normalization failed")) {
		t.Fatal("non-provider failure admitted image repair")
	}
}

func TestImageNormalizationDoesNotSuggestDroppingFields(t *testing.T) {
	t.Parallel()
	for _, status := range []int{400, 422} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			err := ClassifyHTTPError("chat.completions", status, nil,
				[]byte(`{"error":{"message":"Image normalization failed","type":"invalid_request_error"}}`), chatRes(""))
			var providerErr Error
			if !errors.As(err, &providerErr) || providerErr.StatusCode() != status || Kind(err) != KindInvalidRequest {
				t.Fatalf("classification = %v", err)
			}
			if hint := ErrorHint(err); hint != "" {
				t.Fatalf("unrelated field advice attached to an image rejection: %q", hint)
			}
		})
	}
}
