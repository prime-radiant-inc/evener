package registry

import (
	"reflect"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	env := map[string]string{"KEY": "sk-1", "ORG": "org-9"}
	lookup := func(n string) (string, bool) { v, ok := env[n]; return v, ok }
	cases := []struct {
		in, want string
		missing  []string
	}{
		{"plain", "plain", nil},
		{"$KEY", "sk-1", nil},
		{"${KEY}", "sk-1", nil},
		{"Bearer $KEY", "Bearer sk-1", nil},
		{"a$$b", "a$b", nil},
		{"$", "$", nil},
		{"$1", "$1", nil},
		{"$MISSING", "", []string{"MISSING"}},
		{"x-$MISSING-$KEY", "x--sk-1", []string{"MISSING"}},
	}
	for _, c := range cases {
		got, missing := expandEnv(c.in, lookup)
		if got != c.want || !reflect.DeepEqual(missing, c.missing) {
			t.Errorf("expandEnv(%q) = %q, %v; want %q, %v", c.in, got, missing, c.want, c.missing)
		}
	}
}

func TestCheckEnvRefs(t *testing.T) {
	if err := checkEnvRefs("Bearer $KEY and ${OTHER}", "api_key"); err != nil {
		t.Fatal(err)
	}
	if err := checkEnvRefs("${UNTERMINATED", "api_key"); err == nil {
		t.Fatal("unterminated ${ must error")
	}
	if err := checkEnvRefs("${9BAD}", "api_key"); err == nil {
		t.Fatal("invalid name must error")
	}
}
