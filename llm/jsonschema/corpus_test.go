package jsonschema_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

const corpusRevision = "8cdfac41e37527795879e480a483997cbd6188f3"

// TestFixtureCorpus checks committed content, not schema validity. The full
// conformance harness is run by CI, with no fixture downloads at test time.
func TestFixtureCorpus(t *testing.T) {
	t.Logf("JSON-Schema-Test-Suite revision %s", corpusRevision)
	root := "testdata/JSON-Schema-Test-Suite"
	for _, draft := range []string{"draft4", "draft6", "draft7", "draft2019-09", "draft2020-12"} {
		t.Run(draft, func(t *testing.T) {
			var files, groups, cases int
			err := filepath.WalkDir(filepath.Join(root, "tests", draft), func(name string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() || filepath.Ext(name) != ".json" {
					return nil
				}
				data, err := os.ReadFile(name)
				if err != nil {
					return err
				}
				var sets []struct {
					Schema json.RawMessage `json:"schema"`
					Tests  []struct {
						Data  json.RawMessage `json:"data"`
						Valid *bool           `json:"valid"`
					} `json:"tests"`
				}
				if err := json.Unmarshal(data, &sets); err != nil {
					return err
				}
				files++
				groups += len(sets)
				for _, set := range sets {
					if len(set.Schema) == 0 || len(set.Tests) == 0 {
						t.Fatalf("%s: missing schema or declared cases", name)
					}
					for _, test := range set.Tests {
						if len(test.Data) == 0 || test.Valid == nil {
							t.Fatalf("%s: incomplete declared case", name)
						}
					}
					cases += len(set.Tests)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if files == 0 || groups == 0 || cases == 0 {
				t.Fatal("empty corpus")
			}
			t.Logf("files=%d groups=%d cases=%d", files, groups, cases)
		})
	}
	t.Run("remotes", func(t *testing.T) {
		files := 0
		err := filepath.WalkDir(filepath.Join(root, "remotes"), func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(name) != ".json" {
				return nil
			}
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			var schema any
			if err := json.Unmarshal(data, &schema); err != nil {
				return err
			}
			files++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if files == 0 {
			t.Fatal("empty reference fixtures")
		}
		t.Logf("reference files=%d", files)
	})
}
