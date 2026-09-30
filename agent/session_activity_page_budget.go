package agent

import "encoding/json"

type sessionActivityPageBudget struct {
	bytes int
}

func newSessionActivityPageBudget(response any) sessionActivityPageBudget {
	encoded, _ := json.Marshal(response)
	return sessionActivityPageBudget{bytes: len(encoded)}
}

func (budget *sessionActivityPageBudget) fits(_ any, response any) bool {
	encoded, _ := json.Marshal(response)
	if len(encoded) > sessionActivityPageBytes-2048 {
		return false
	}
	budget.bytes = len(encoded)
	return true
}
