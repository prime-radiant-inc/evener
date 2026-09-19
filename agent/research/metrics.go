package research

import (
	"encoding/json"
	"fmt"
	"os"

	"primeradiant.com/evener/agent/internal/atif"
)

// AtifMetrics summarizes one rollout's recorded traffic.
type AtifMetrics struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	Steps            int `json:"steps"`
	ModelRequests    int `json:"model_requests"`
}

// ExtractAtifMetrics decodes an ATIF v1.7 export written by `evener run -n`.
func ExtractAtifMetrics(path string) (AtifMetrics, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return AtifMetrics{}, fmt.Errorf("read atif export: %w", err)
	}
	var traj atif.Trajectory
	if err := json.Unmarshal(data, &traj); err != nil {
		return AtifMetrics{}, fmt.Errorf("decode atif export: %w", err)
	}
	m := AtifMetrics{Steps: len(traj.Steps)}
	if traj.FinalMetrics != nil {
		m.PromptTokens = traj.FinalMetrics.TotalPromptTokens
		m.CompletionTokens = traj.FinalMetrics.TotalCompletionTokens
		m.CachedTokens = traj.FinalMetrics.TotalCachedTokens
		m.Steps = traj.FinalMetrics.TotalSteps
		if m.Steps == 0 {
			m.Steps = len(traj.Steps)
		}
	}
	for _, s := range traj.Steps {
		if s.Metrics != nil {
			m.ModelRequests++
		}
	}
	return m, nil
}
