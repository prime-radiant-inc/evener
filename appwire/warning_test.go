package appwire

import "testing"

func TestWarningParamsEffectiveMessage(t *testing.T) {
	tests := []struct {
		name   string
		params WarningParams
		want   string
	}{
		{
			name:   "message set wins even when warning also carries one",
			params: WarningParams{Message: "top-level message", Warning: "provider hiccup"},
			want:   "top-level message",
		},
		{
			name:   "bare-string warning",
			params: WarningParams{Warning: "provider hiccup"},
			want:   "provider hiccup",
		},
		{
			name:   "object-form warning",
			params: WarningParams{Warning: map[string]any{"message": "nested"}},
			want:   "nested",
		},
		{
			name:   "neither message nor warning",
			params: WarningParams{},
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.params.EffectiveMessage(); got != tc.want {
				t.Fatalf("EffectiveMessage() = %q, want %q", got, tc.want)
			}
		})
	}
}
