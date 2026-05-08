package openai

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
)

const randomTokenBytes = 32

// GeneratePKCE creates a verifier and its S256 code challenge.
func GeneratePKCE() (string, string, error) {
	verifierBytes := make([]byte, randomTokenBytes)
	if _, err := rand.Read(verifierBytes); err != nil {
		return "", "", err
	}

	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes)
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])

	return verifier, challenge, nil
}

// GenerateState creates a random OAuth state token.
func GenerateState() (string, error) {
	stateBytes := make([]byte, randomTokenBytes)
	if _, err := rand.Read(stateBytes); err != nil {
		return "", err
	}

	return base64.RawURLEncoding.EncodeToString(stateBytes), nil
}
