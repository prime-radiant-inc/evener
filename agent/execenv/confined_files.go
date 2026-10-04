package execenv

import (
	"fmt"
	"os"
	"path/filepath"
)

// NewConfinedFileEnvironment grants file operations only beneath relativeRoot.
// The host anchor is trusted; its relative tail is created by shared confinement.
func NewConfinedFileEnvironment(stateRoot, relativeRoot string) (*LocalExecutionEnvironment, error) {
	if stateRoot == "" || !filepath.IsAbs(stateRoot) {
		return nil, fmt.Errorf("memory state root must be absolute")
	}
	if !filepath.IsLocal(relativeRoot) {
		return nil, fmt.Errorf("file root must remain beneath its host anchor")
	}
	stateRoot = filepath.Clean(stateRoot)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, err
	}
	host := newScratchSandboxFS(stateRoot)
	defer host.retire()
	root := filepath.Join(stateRoot, relativeRoot)
	if err := host.mkdirAll("memory", root); err != nil {
		return nil, err
	}
	env := NewLocalExecutionEnvironment(root)
	layer := newScratchSandboxFS(root)
	// Open beneath the already-captured host anchor, never through the scope's
	// host path. Transfer this fd to the lasting scope layer before retiring host.
	fd, err := openBeneathRoot(host.rootFds[stateRoot], filepath.ToSlash(filepath.Clean(relativeRoot)), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	layer.rootFds[root] = fd
	env.Sandbox = layer.policy
	// Keep the layer so all operations share the same root-fd lifetime.
	env.sbfs = layer
	return env, nil
}
