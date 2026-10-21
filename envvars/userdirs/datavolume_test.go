package userdirs

import "testing"

func TestStripDataVolumeAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "prefix root", in: "/System/Volumes/Data", want: "/"},
		{name: "plain home", in: "/System/Volumes/Data/Users/fixture", want: "/Users/fixture"},
		{name: "cleaned dotdot", in: "/System/Volumes/Data/Users/fixture/../other", want: "/Users/other"},
		{name: "trailing separator", in: "/System/Volumes/Data/Users/fixture/", want: "/Users/fixture"},
		{name: "double separator", in: "/System/Volumes/Data//Users//fixture", want: "/Users/fixture"},
		{name: "prefix sibling", in: "/System/Volumes/DataBackup/Users/fixture", want: "/System/Volumes/DataBackup/Users/fixture"},
		{name: "prefix similar", in: "/System/Volumes/Database/Users/fixture", want: "/System/Volumes/Database/Users/fixture"},
		{name: "plain unchanged", in: "/Users/fixture", want: "/Users/fixture"},
		{name: "relative cleaned", in: "../fixture", want: "../fixture"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripDataVolumeAlias(tc.in); got != tc.want {
				t.Fatalf("StripDataVolumeAlias(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
