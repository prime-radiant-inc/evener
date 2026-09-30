package agent

import "encoding/json"

type sessionActivityPageBudget struct {
	bytes    int
	rows     int
	fallback bool
}

func newSessionActivityPageBudget(response any) sessionActivityPageBudget {
	encoded, err := json.Marshal(response)
	return sessionActivityPageBudget{bytes: len(encoded), fallback: err != nil}
}

// The typed empty response owns the fixed envelope and array brackets. Each
// admitted row adds only its JSON bytes and, after the first row, one comma.
func (budget *sessionActivityPageBudget) fits(row, response any) bool {
	if !budget.fallback {
		encoded, err := json.Marshal(row)
		if err == nil {
			candidateBytes := budget.bytes + len(encoded)
			if budget.rows > 0 {
				candidateBytes++
			}
			if candidateBytes > sessionActivityPageBytes-2048 {
				return false
			}
			budget.bytes = candidateBytes
			budget.rows++
			return true
		}
		// An admitted unencodable row remains in subsequent prefixes. Preserve
		// the full-response probe's error-ignored result for this entire page.
		budget.fallback = true
	}
	encoded, _ := json.Marshal(response)
	if len(encoded) > sessionActivityPageBytes-2048 {
		return false
	}
	budget.bytes = len(encoded)
	return true
}
