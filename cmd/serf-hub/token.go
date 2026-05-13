package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

func newHubToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate hub token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}
