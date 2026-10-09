package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
)

// RemoveConfinedFile removes only an admitted regular file, without reading it.
// It requires captured filesystem authority, never an unrestricted fallback.
func (e *LocalExecutionEnvironment) RemoveConfinedFile(path string) error {
	layer := e.sandbox()
	if layer == nil {
		return errors.New("file removal requires a confined environment")
	}
	defer layer.release()
	return layer.removeRegularFile("memory_delete", e.resolve(path))
}

// LinkConfinedFile gives the file at oldPath the second name newPath, never
// replacing a file: an existing newPath fails with an error matching
// fs.ErrExist. It requires captured filesystem authority, never an
// unrestricted fallback.
func (e *LocalExecutionEnvironment) LinkConfinedFile(oldPath, newPath string) error {
	layer := e.sandbox()
	if layer == nil {
		return errors.New("file linking requires a confined environment")
	}
	defer layer.release()
	return layer.link("memory_migrate", e.resolve(oldPath), e.resolve(newPath))
}

// NewConfinedFileEnvironment grants file operations only beneath relativeRoot.
// The host anchor is trusted; its relative tail is created by shared confinement.
func NewConfinedFileEnvironment(stateRoot, relativeRoot string) (*LocalExecutionEnvironment, error) {
	root, err := NewConfinedFileRoot(stateRoot, relativeRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(nil)
}

// ConfinedFileRoot retains host authority while memory storage is requalified.
// Ordinary environments still retain their original scope fd for their lifetime.
type ConfinedFileRoot struct {
	mu                      sync.Mutex
	host                    *sandboxFS
	stateRoot, relativeRoot string
}

// NewConfinedFileRoot captures the host anchor for later scope requalification.
func NewConfinedFileRoot(stateRoot, relativeRoot string) (*ConfinedFileRoot, error) {
	if stateRoot == "" || !filepath.IsAbs(stateRoot) {
		return nil, errors.New("memory state root must be absolute")
	}
	if !filepath.IsLocal(relativeRoot) {
		return nil, errors.New("file root must remain beneath its host anchor")
	}
	stateRoot = filepath.Clean(stateRoot)
	if err := os.MkdirAll(stateRoot, 0o700); err != nil {
		return nil, err
	}
	host := newScratchSandboxFS(stateRoot)
	if _, err := host.rootFd(stateRoot); err != nil {
		host.retire()
		return nil, err
	}
	return &ConfinedFileRoot{host: host, stateRoot: stateRoot, relativeRoot: filepath.Clean(relativeRoot)}, nil
}

// Close retires the captured host authority without deleting stored files.
func (r *ConfinedFileRoot) Close() {
	r.mu.Lock()
	host := r.host
	r.host = nil
	r.mu.Unlock()
	host.retire()
}

// Open creates the fixed tail if needed and reuses previous only when the tail
// still names the same directory. The caller owns previous's lifetime,
// including admitted operations on it.
func (r *ConfinedFileRoot) Open(previous *LocalExecutionEnvironment) (*LocalExecutionEnvironment, error) {
	r.mu.Lock()
	host := r.host
	if host == nil {
		r.mu.Unlock()
		return nil, errors.New("file root is closed")
	}
	host.acquire()
	r.mu.Unlock()
	defer host.release()
	root := filepath.Join(r.stateRoot, r.relativeRoot)
	if err := host.mkdirAll("memory", root); err != nil {
		return nil, err
	}
	fd, err := openBeneathRoot(host.rootFds[r.stateRoot], filepath.ToSlash(r.relativeRoot), os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	// Stat a separately opened descriptor, never the host path. The temporary
	// os.File owns only its duplicate, not the fd transferred to the layer.
	info, err := confinedDirectoryInfo(fd)
	if err != nil {
		_ = os.NewFile(uintptr(fd), root).Close()
		return nil, err
	}
	if confinedEnvironmentMatches(previous, root, info) {
		_ = os.NewFile(uintptr(fd), root).Close()
		return previous, nil
	}
	env := NewLocalExecutionEnvironment(root)
	layer := newScratchSandboxFS(root)
	layer.rootFds[root] = fd
	env.Sandbox = layer.policy
	// Keep the layer so all operations share the same root-fd lifetime.
	env.sbfs = layer
	return env, nil
}

func confinedEnvironmentMatches(previous *LocalExecutionEnvironment, root string, info os.FileInfo) bool {
	if previous == nil {
		return false
	}
	previous.sbMu.Lock()
	layer := previous.sbfs
	layer.acquire()
	previous.sbMu.Unlock()
	defer layer.release()
	if layer == nil {
		return false
	}
	oldFd, err := layer.rootFd(root)
	if err != nil {
		return false
	}
	oldInfo, err := confinedDirectoryInfo(oldFd)
	if err != nil {
		return false
	}
	return os.SameFile(info, oldInfo)
}

func confinedDirectoryInfo(fd int) (os.FileInfo, error) {
	dup, err := openBeneathRoot(fd, ".", os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "confined-root")
	defer func() { _ = f.Close() }()
	return f.Stat()
}
