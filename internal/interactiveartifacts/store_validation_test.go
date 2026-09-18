package interactiveartifacts

import (
	"errors"
	"strings"
	"testing"
)

func TestMutationValidationDomainErrors(t *testing.T) {
	for _, tt := range []struct {
		name, tool, raw string
		code            ErrorCode
	}{
		{"empty source", "artifact_publish", `{"mutationId":"M","title":"","summary":"","html":""}`, InvalidSource},
		{"unsupported format", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","format":"markdown"}`, UnsupportedFormat},
		{"unsupported version", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","formatVersion":2}`, UnsupportedFormat},
		{"source size", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"` + strings.Repeat("x", MaxSourceBytes+1) + `"}`, TooLarge},
		{"initial state shape", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","initialState":null}`, InvalidState},
		{"initial state number", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","initialState":{"n":1e400}}`, InvalidState},
		{"state size", "artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":{"s":"` + strings.Repeat("x", MaxStateBytes) + `"}}`, TooLarge},
		{"state number", "artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":{"n":9007199254740993}}`, InvalidState},
		{"duplicate state", "artifact_save_state", `{"artifactId":"A","mutationId":"M","expectedSourceRevision":1,"expectedStateVersion":1,"state":{"n":1,"n":2}}`, InvalidState},
		{"source request shape", "artifact_publish", `{"mutationId":"M","title":"T","summary":"S","html":"H","namespaceId":"forged"}`, InvalidSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseRequest(tt.tool, []byte(tt.raw), false)
			var domain *DomainError
			if !errors.As(err, &domain) || domain.Code != tt.code {
				t.Fatalf("want %s, got %v", tt.code, err)
			}
		})
	}
}
