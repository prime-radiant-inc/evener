package appwire

import "testing"

func TestProtocolVersionNumber(t *testing.T) {
	for _, tc := range []struct {
		version string
		number  int
		ok      bool
	}{
		{"evener-appwire-v0", 0, true},
		{"evener-appwire-v5", 5, true},
		{"evener-appwire-v7", 7, true},
		{"evener-appwire-v12", 12, true},
		{"", 0, false},
		{"evener-appwire-", 0, false},
		{"evener-appwire-v", 0, false},
		{"evener-appwire-v05", 0, false},
		{"evener-appwire-v5-rc1", 0, false},
		{"evener-appwire-v-1", 0, false},
		{"old-protocol", 0, false},
	} {
		number, ok := ProtocolVersionNumber(tc.version)
		if number != tc.number || ok != tc.ok {
			t.Errorf("ProtocolVersionNumber(%q) = %d, %v; want %d, %v", tc.version, number, ok, tc.number, tc.ok)
		}
	}
}

func TestOlderAndNewerProtocolVersion(t *testing.T) {
	for _, tc := range []struct {
		version string
		older   bool
		newer   bool
	}{
		{"evener-appwire-v5", true, false},
		{ProtocolVersion, false, false},
		{"evener-appwire-v8", false, true},
		{"old-protocol", false, false},
		{"evener-appwire-v05", false, false},
	} {
		if got := OlderProtocolVersion(tc.version); got != tc.older {
			t.Errorf("OlderProtocolVersion(%q) = %v, want %v", tc.version, got, tc.older)
		}
		if got := NewerProtocolVersion(tc.version); got != tc.newer {
			t.Errorf("NewerProtocolVersion(%q) = %v, want %v", tc.version, got, tc.newer)
		}
	}
}
