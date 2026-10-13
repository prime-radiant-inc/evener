package main

import (
	"errors"
	"testing"
)

func TestParseAPILog(t *testing.T) {
	for _, tc := range []struct {
		value   string
		want    bool
		wantErr bool
	}{
		{value: "", want: false},
		{value: "off", want: false},
		{value: "OFF", want: false},
		{value: " off ", want: false},
		{value: "on", want: true},
		{value: "ON", want: true},
		{value: " On ", want: true},
		{value: "maybe", wantErr: true},
		{value: "1", wantErr: true},
		{value: "true", wantErr: true},
	} {
		t.Run(tc.value, func(t *testing.T) {
			got, err := parseAPILog(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("parseAPILog(%q) = %v, want error", tc.value, got)
				}
				if !errors.Is(err, errInvalidAPILog) {
					t.Fatalf("parseAPILog(%q) error = %v, want errInvalidAPILog", tc.value, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseAPILog(%q): %v", tc.value, err)
			}
			if got != tc.want {
				t.Fatalf("parseAPILog(%q) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}
