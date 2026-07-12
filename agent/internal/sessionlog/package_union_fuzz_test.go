//go:build serffuzz

package sessionlog

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
)

func FuzzSessionLogPackageUnion(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		log, err := newSessionLogFS("/logs/session.jsonl", afero.NewMemMapFs())
		if err != nil {
			t.Fatal(err)
		}
		want := errors.New("marshal session entry")
		log.marshal = func(any) ([]byte, error) { return nil, want }
		if err := log.Append(SessionLogEntry{Action: "fuzz"}); !errors.Is(err, want) {
			t.Fatalf("Append error = %v, want %v", err, want)
		}
	})
}
