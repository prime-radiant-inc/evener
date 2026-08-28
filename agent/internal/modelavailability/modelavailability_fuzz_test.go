package modelavailability

import "testing"

// FuzzCursorDecode keeps the authenticated continuation parser total for
// arbitrary model-facing cursor bytes.
// Registry: native:agent:./internal/modelavailability:FuzzCursorDecode::modelavailability.go
func FuzzCursorDecode(f *testing.F) {
	f.Add("not-a-cursor")
	f.Add("")
	s := testSnapshot("v1", true, "provider/model")
	f.Fuzz(func(t *testing.T, token string) {
		_, _ = s.Page(token, 1, 64)
	})
}
