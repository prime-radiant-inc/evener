package openai_test

import (
	"errors"
	"fmt"

	"primeradiant.com/serf/auth/openai"
)

// Example shows the typical entry points: build a Service from the default
// config, then check whether the instance already has a usable credential
// before deciding to start a login flow. No network call is made.
func Example() {
	svc := openai.NewService(openai.DefaultConfig(), nil)

	stateDir := openai.DefaultStateDir()
	const instanceName = "openai"

	// Inspecting the stored record directly distinguishes "never logged in"
	// from a corrupt file.
	if _, err := openai.LoadAuth(stateDir, instanceName); errors.Is(err, openai.ErrAuthNotFound) {
		fmt.Println("no stored OAuth record yet")
	}

	// Status reports the effective state (stored OAuth record, OPENAI_API_KEY,
	// or signed out) without touching the network.
	status, err := svc.Status(stateDir, instanceName)
	if err != nil {
		return
	}
	if !status.SignedIn {
		// At this point a caller would run svc.Login or svc.LoginWithDevice.
		_ = openai.AuthSourceSignedOut
	}
}
