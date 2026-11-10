package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"primeradiant.com/evener/agent/internal/tool"
)

// readStamp identifies the state of a file at the moment it was read.
type readStamp struct {
	size  int64
	mtime time.Time
}

// priorRead is a read_file call a session has already made: the file's state
// when it was read and the turn that read happened in.
type priorRead struct {
	stamp readStamp
	turn  int
}

// readRangeKey names one path and line range as the caller asked for it.
type readRangeKey struct {
	path          string
	offset, limit int
}

// readFileKey returns the key for a read_file call, or false when the call's
// arguments do not name a path.
func (s *Session) readFileKey(call []byte) (readRangeKey, bool) {
	var args map[string]any
	if json.Unmarshal(call, &args) != nil {
		return readRangeKey{}, false
	}
	path, _ := args["file_path"].(string)
	if path == "" {
		return readRangeKey{}, false
	}
	key := readRangeKey{path: s.resolveFilePath(path)}
	if v := optionalIntArg(args, "offset"); v != nil {
		key.offset = *v
	}
	if v := optionalIntArg(args, "limit"); v != nil {
		key.limit = *v
	}
	return key, true
}

func statReadStamp(path string) (readStamp, bool) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return readStamp{}, false
	}
	return readStamp{size: info.Size(), mtime: info.ModTime()}, true
}

// noteReadFileRepeat marks a successful read_file result as unchanged when this
// session already read the same path and range from a file whose size and
// mtime still match, then records this read. before is the file's stamp taken
// ahead of the read, so a write racing the read invalidates the next compare
// instead of being hidden by it.
func (s *Session) noteReadFileRepeat(key readRangeKey, before readStamp, res *tool.ExecResult) {
	s.mu.Lock()
	turn := s.totalRounds
	s.mu.Unlock()

	s.readFilesMu.Lock()
	prior, seen := s.priorReads[key]
	if s.priorReads == nil {
		s.priorReads = map[readRangeKey]priorRead{}
	}
	s.priorReads[key] = priorRead{stamp: before, turn: turn}
	s.readFilesMu.Unlock()

	if seen && prior.stamp.size == before.size && prior.stamp.mtime.Equal(before.mtime) {
		res.UnchangedSinceTurn = &prior.turn
		res.Output += fmt.Sprintf("\n[Unchanged since your read at turn %d: same file, same range.]", prior.turn)
	}
}
