package main

import "primeradiant.com/evener/agent"

// launchSettings are the values `evener` and `evener serve` derive from the
// shared launch flags before either builds a client or session.
type launchSettings struct {
	apiLogEnabled               bool
	openAIResponsesContinuation string
}

// resolveLaunchSettings validates the provider idle timeout and API-log flags
// and resolves the OpenAI Responses continuation mode. A bad value fails the
// launch before any state is created.
func resolveLaunchSettings(providerIdleTimeout, apiLog, openAIResponsesContinuation string) (launchSettings, error) {
	if _, err := agent.ParseProviderIdleTimeout(providerIdleTimeout); err != nil {
		return launchSettings{}, err
	}
	apiLogEnabled, err := parseAPILog(apiLog)
	if err != nil {
		return launchSettings{}, err
	}
	return launchSettings{
		apiLogEnabled:               apiLogEnabled,
		openAIResponsesContinuation: resolveOpenAIResponsesContinuation(openAIResponsesContinuation, nil),
	}, nil
}
