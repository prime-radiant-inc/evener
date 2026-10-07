package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"path/filepath"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
)

func registerMemoryTools(reg *tool.Registry, s *Session) error {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return nil
	}
	tools := []tool.RegisteredTool{
		{Definition: tool.MemoryDefinition(tool.DefReadFile(), "memory_read"), ReadOnly: true, Exec: s.execMemoryRead},
		{Definition: tool.MemoryDefinition(tool.DefWriteFile(), "memory_write"), Exec: s.execMemoryWrite},
		{Definition: tool.MemoryDefinition(tool.DefEditFile(), "memory_edit"), Exec: s.execMemoryEdit},
		{Definition: tool.MemoryDefinition(tool.DefGrep(), "memory_search"), ReadOnly: true, Exec: s.execMemorySearch},
		{Definition: tool.DefMemoryDelete(), Exec: s.execMemoryDelete},
	}
	for _, registered := range tools {
		if err := reg.Register(registered); err != nil {
			return err
		}
	}
	return nil
}

// memoryFileArgs changes only path authority; shared executors own file semantics.
func (s *Session) memoryFileArgs(args map[string]any, key, operation string) (*execenv.LocalExecutionEnvironment, map[string]any, func(), error) {
	scope := stringArg(args, "scope")
	if _, err := s.memoryScopeBinding(scope); err != nil {
		return nil, nil, nil, err
	}
	path := stringArg(args, key)
	if key == "path" && path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) {
		return nil, nil, nil, errors.New("memory path must be relative and remain in its scope")
	}
	env, release, err := s.acquireMemoryEnvironment(scope)
	if err != nil {
		return nil, nil, nil, err
	}
	forwarded := maps.Clone(args)
	forwarded[key] = filepath.Join(env.WorkingDirectory(), path)
	if err := s.beforeMemoryIO(scope, operation); err != nil {
		release()
		return nil, nil, nil, err
	}
	return env, forwarded, release, nil
}

// execOwnMemoryWrite runs a write, edit or delete of one memory file and,
// once it succeeds, records the result as the session's own: a change to the
// index becomes its baseline and a change to a page it read becomes that
// page's record, so neither is echoed back.
func (s *Session) execOwnMemoryWrite(args map[string]any, operation string, write func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error)) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", operation)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := write(env, forwarded)
	if err == nil {
		s.recordOwnMemoryWrite(env, scope, file)
	}
	return out, err
}

func (s *Session) execMemoryWrite(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "write", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error) {
		return execFileWrite(ctx, env, forwarded, s.fileReadGuard(env))
	})
}

// memoryPageSizeLimit is the size, in bytes, past which memory_read notes that
// a page is long.
const memoryPageSizeLimit = 4096

// memoryPageSizeNote is the note memory_read appends to a page of size bytes.
// It points at the gardening-memory skill only when the session can use it;
// otherwise it says what a page should hold.
func memoryPageSizeNote(size int, withSkill bool) string {
	kb := int(math.Round(float64(size) / 1024))
	if withSkill {
		return fmt.Sprintf("\n\nThis page is long (%d KB). Use the gardening-memory skill to learn how to fix it.", kb)
	}
	return fmt.Sprintf("\n\nThis page is long (%d KB). A memory page should hold one fact.", kb)
}

// memoryGardeningSkillAvailable reports whether the session can load the
// gardening-memory skill: use_skill is callable and the skill is advertised
// to the model.
func (s *Session) memoryGardeningSkillAvailable() bool {
	if !s.canInstructTool("use_skill") {
		return false
	}
	for _, descriptor := range s.skills.ModelEntries() {
		if descriptor.CatalogName == "gardening-memory" {
			return true
		}
	}
	return false
}

func (s *Session) execMemoryRead(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", "read")
	if err != nil {
		return nil, err
	}
	defer release()
	// Capture the bytes this read loaded, so the page's record is exactly what
	// the session saw. They are the whole page even for an offset or limit
	// read, which therefore records the whole page as of that read.
	var raw []byte
	out, err := execFileReadWith(forwarded, s.fileReadGuard(env), func(path string, offset, limit *int) (string, error) {
		text, loaded, err := env.ReadFileAndBytes(path, offset, limit)
		raw = loaded
		return text, err
	})
	// The index has its own baseline and change blocks; every other page
	// read is tracked for change notices.
	if err == nil && file != memoryIndexFile {
		size := len(raw)
		// Another session may change the page between the read and its
		// record; the record still holds what this read loaded.
		recordErr := s.beforeMemoryIO(scope, "record")
		if recordErr != nil {
			raw = nil
		}
		s.recordMemoryContent(scope, file, raw, recordErr, true)
		// A long page gets a note pointing at what a page should be.
		if text, ok := out.(string); ok && size > memoryPageSizeLimit {
			out = text + memoryPageSizeNote(size, s.memoryGardeningSkillAvailable())
		}
	}
	return out, err
}
func (s *Session) execMemoryEdit(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "edit", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error) {
		return execFileEdit(ctx, env, forwarded, s.fileReadGuard(env))
	})
}
func (s *Session) execMemorySearch(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, forwarded, release, err := s.memoryFileArgs(args, "path", "search")
	if err != nil {
		return nil, err
	}
	defer release()
	return execFileGrep(ctx, env, forwarded)
}
func (s *Session) execMemoryDelete(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "delete", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error) {
		path := stringArg(forwarded, "file_path")
		warn := s.fileReadGuard(env).ReadBeforeWriteWarning(path)
		if err := env.RemoveConfinedFile(path); err != nil {
			return nil, err
		}
		return warn + "Removed or already absent: " + path, nil
	})
}
