//go:build !unix

package sandbox

import (
	"errors"
	"os"
)

// childEnvFile has no private-fd implementation outside Unix; the confined MCP
// spawn sites only exist on Unix, so a call here is a programming error rather
// than a runtime condition.
func childEnvFile(Backend, ChildEnvDelta) (*os.File, error) {
	return nil, errors.New("sandbox: private child-env fd is unsupported on this platform")
}
