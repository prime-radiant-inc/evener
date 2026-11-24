package appwire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type rankFixture struct {
	Cases []struct {
		Name          string           `json:"name"`
		Query         string           `json:"query"`
		Limit         int              `json:"limit"`
		Items         []FileSearchItem `json:"items"`
		WantPaths     []string         `json:"wantPaths"`
		WantPositions map[string][]int `json:"wantPositions"`
	} `json:"cases"`
}

func TestRankFileSearchItems(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "testdata", "filesearch", "rank_cases.json"))
	if err != nil {
		t.Fatalf("read rank fixture: %v", err)
	}
	var fixture rankFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("unmarshal rank fixture: %v", err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("rank fixture has no cases")
	}

	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			got := RankFileSearchItems(tc.Items, tc.Query, tc.Limit)
			if got == nil {
				t.Fatal("RankFileSearchItems returned nil slice, want non-nil")
			}
			gotPaths := make([]string, len(got))
			for i, item := range got {
				gotPaths[i] = item.Path
			}
			if !slices.Equal(gotPaths, tc.WantPaths) {
				t.Fatalf("RankFileSearchItems paths = %v, want %v", gotPaths, tc.WantPaths)
			}
			for path, wantPos := range tc.WantPositions {
				_, gotPos, ok := ScoreFileMention(path, tc.Query)
				if !ok {
					t.Fatalf("ScoreFileMention(%q, %q) returned ok=false, want positions %v", path, tc.Query, wantPos)
				}
				if !slices.Equal(gotPos, wantPos) {
					t.Fatalf("ScoreFileMention(%q, %q) positions = %v, want %v", path, tc.Query, gotPos, wantPos)
				}
			}
		})
	}
}

func TestFileSearchQueryIncludesDotfiles(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{query: "", want: false},
		{query: "./src", want: false},
		{query: "src/main.go", want: false},
		{query: "app.config.ts", want: false},
		{query: "./.env", want: true},
		{query: ".github/w", want: true},
		{query: "a/.b", want: true},
		{query: ".", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			norm := NormalizeFileSearchQuery(tc.query)
			if got := FileSearchQueryIncludesDotfiles(norm); got != tc.want {
				t.Fatalf("FileSearchQueryIncludesDotfiles(Normalize(%q)=%q) = %v, want %v", tc.query, norm, got, tc.want)
			}
		})
	}
}
