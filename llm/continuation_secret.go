package llm

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

var ErrContinuationSecretUnavailable = errors.New("continuation secret unavailable")

func ContinuationSecretPath(stateDir string) string {
	if stateDir == "" {
		return ""
	}
	return filepath.Join(stateDir, "continuation", "local_scope_secret")
}

func LoadOrCreateContinuationSecret(stateDir string) ([]byte, error) {
	path := ContinuationSecretPath(stateDir)
	if path == "" {
		return nil, fmt.Errorf("%w: missing state dir", ErrContinuationSecretUnavailable)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("%w: create secret dir: %v", ErrContinuationSecretUnavailable, err)
	}

	secret, err := readContinuationSecret(path)
	if err == nil {
		return secret, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}

	secret = make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("%w: generate secret: %v", ErrContinuationSecretUnavailable, err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readContinuationSecret(path)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: create secret file: %v", ErrContinuationSecretUnavailable, err)
	}
	defer f.Close() //nolint:errcheck
	if _, err := f.Write(secret); err != nil {
		return nil, fmt.Errorf("%w: write secret file: %v", ErrContinuationSecretUnavailable, err)
	}
	if err := f.Sync(); err != nil {
		return nil, fmt.Errorf("%w: sync secret file: %v", ErrContinuationSecretUnavailable, err)
	}
	return secret, nil
}

func readContinuationSecret(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode().Perm() != 0o600 {
		return nil, fmt.Errorf("%w: secret file mode %o", ErrContinuationSecretUnavailable, info.Mode().Perm())
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read secret file: %v", ErrContinuationSecretUnavailable, err)
	}
	if len(secret) != 32 {
		return nil, fmt.Errorf("%w: secret length %d", ErrContinuationSecretUnavailable, len(secret))
	}
	return secret, nil
}
