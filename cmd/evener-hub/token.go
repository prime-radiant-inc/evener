package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

var hubTokenRead = rand.Read

func newHubToken() (string, error) {
	var raw [32]byte
	if _, err := hubTokenRead(raw[:]); err != nil {
		return "", fmt.Errorf("generate hub token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
