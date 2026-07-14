package identifier

import "testing"

func FuzzProjectIDFormat(f *testing.F) {
	f.Add("/tmp/project")
	f.Add("relative/path")
	f.Add("/tmp/strange ! path")
	f.Fuzz(func(t *testing.T, path string) {
		if path == "" {
			return
		}
		id := projectID(path)
		if len(id) == 0 || len(id) > 80 {
			t.Fatalf("projectID(%q) = %q, length %d", path, id, len(id))
		}
		if err := ValidateProjectID(id); err != nil {
			t.Fatalf("projectID(%q) = %q: %v", path, id, err)
		}
	})
}
