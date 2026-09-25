package schema

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// bootGenerationSuffix names a session's boot counter beside its meta.
const bootGenerationSuffix = ".boot-generation"

// BootGenerationPath is where the session's boot counter lives:
// <dir>/sessions/<id>.boot-generation.
func BootGenerationPath(dir, id string) string {
	return filepath.Join(dir, sessionsSubdir, id+bootGenerationSuffix)
}

// NextBootGeneration increments the session's boot counter and returns the
// new value: one past the larger of the stored counter (0 when none) and
// floor. A daemon start passes floor 0, so its first start serves at 1; a
// session that takes over a ref already served at some generation (a
// thread/clear replacement) passes that generation, so clients holding it
// replace rather than ignore the new session's history. A daemon calls it
// once per start, after it owns the session (its ownership lock is what
// serializes the read-increment-write), and before it serves any read or
// notification. The
// new value is durable when it returns: written to a temp file, fsynced,
// renamed over the counter, and the directory fsynced. So a crash that loses
// buffered transcript entries can never be followed by a start that reuses a
// generation clients already hold. A counter that does not parse is an
// error rather than a restart at 1, which would move the generation
// backwards.
func NextBootGeneration(dir, id string, floor uint64) (uint64, error) {
	if err := ValidateSessionID(id); err != nil {
		return 0, err
	}
	sessDir := filepath.Join(dir, sessionsSubdir)
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		return 0, fmt.Errorf("create sessions dir: %w", err)
	}
	path := BootGenerationPath(dir, id)
	var current uint64
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		current, err = strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("session %s boot generation %s: %w", id, path, err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return 0, fmt.Errorf("read boot generation: %w", err)
	}
	current = max(current, floor)
	if current == math.MaxUint64 {
		return 0, fmt.Errorf("session %s boot generation overflow", id)
	}
	next := current + 1
	if err := writeFileDurably(path, []byte(strconv.FormatUint(next, 10)+"\n")); err != nil {
		return 0, fmt.Errorf("write boot generation: %w", err)
	}
	return next, nil
}

// writeFileDurably replaces path with data through a fsynced temp file and a
// rename, then fsyncs the directory so the rename itself survives power loss.
func writeFileDurably(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	syncErr := dir.Sync()
	closeErr := dir.Close()
	return errors.Join(syncErr, closeErr)
}
