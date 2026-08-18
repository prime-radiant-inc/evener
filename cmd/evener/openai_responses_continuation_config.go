package main

import (
	"strings"

	"primeradiant.com/evener/envvars"
)

func resolveOpenAIResponsesContinuation(flagValue string, getenv func(string) string) string {
	if trimmed := strings.TrimSpace(flagValue); trimmed != "" {
		return trimmed
	}
	return envvars.SERFOpenAIResponsesContinuation.FromTrimmed(getenv)
}
