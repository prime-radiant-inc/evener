package skill

import "testing"

func TestIsSlashAddressableName(t *testing.T) {
	long := make([]byte, 128)
	for i := range long {
		long[i] = 'a'
	}
	tooLong := append(append([]byte{}, long...), 'a')
	cases := []struct {
		name string
		want bool
	}{
		{"a", true},
		{"skill-2_name", true},
		{"plugin:skill", true},
		{string(long), true},
		{"", false},
		{"-skill", false},
		{"_skill", true},
		{"plugin:one:skill", false},
		{"skill name", false},
		{"Skill", true},
		{string(tooLong), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSlashAddressableName(tc.name); got != tc.want {
				t.Fatalf("IsSlashAddressableName(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
