package agent

import (
	"slices"
	"testing"
)

func TestBootGenerationPublishesInstalledFrontierGeneration(t *testing.T) {
	t.Parallel()
	for _, publish := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-publisher", true: "with-publisher"}[publish], func(t *testing.T) {
			s := &Session{}
			var published []string
			if publish {
				s.transcriptFrontier.publish = func(generation string) {
					if !s.mu.TryLock() {
						t.Error("frontier publication held Session.mu")
						return
					}
					s.mu.Unlock()
					if installed := s.BootGeneration(); installed != generation {
						t.Errorf("published generation=%q installed=%q", generation, installed)
					}
					published = append(published, generation)
				}
			}
			generations := []string{"7", "8"}
			for _, generation := range generations {
				s.SetBootGeneration(generation)
				if got := s.BootGeneration(); got != generation {
					t.Fatalf("installed generation=%q want=%q", got, generation)
				}
			}
			if publish && !slices.Equal(published, generations) {
				t.Fatalf("published generations=%v want=%v", published, generations)
			}
		})
	}
}
