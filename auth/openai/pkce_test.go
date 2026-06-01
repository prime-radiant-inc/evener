package openai

import (
	"crypto/sha256"
	"encoding/base64"
	"regexp"
	"testing"
)

var base64URLPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func TestGeneratePKCEProducesVerifierAndChallenge(t *testing.T) {
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		t.Fatalf("GeneratePKCE() error = %v", err)
	}

	if len(verifier) < 43 || len(verifier) > 128 {
		t.Fatalf("verifier length = %d, want 43..128", len(verifier))
	}
	if !base64URLPattern.MatchString(verifier) {
		t.Fatalf("verifier %q is not unpadded base64url", verifier)
	}

	sum := sha256.Sum256([]byte(verifier))
	wantChallenge := base64.RawURLEncoding.EncodeToString(sum[:])
	if challenge != wantChallenge {
		t.Fatalf("challenge = %q, want %q", challenge, wantChallenge)
	}
}

func TestGenerateStateProducesRandomBase64URLState(t *testing.T) {
	first, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() first error = %v", err)
	}
	second, err := GenerateState()
	if err != nil {
		t.Fatalf("GenerateState() second error = %v", err)
	}

	if len(first) < 32 {
		t.Fatalf("state length = %d, want at least 32", len(first))
	}
	if !base64URLPattern.MatchString(first) {
		t.Fatalf("state %q is not unpadded base64url", first)
	}
	if first == second {
		t.Fatalf("two generated states matched: %q", first)
	}
}
