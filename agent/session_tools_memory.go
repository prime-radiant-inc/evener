package agent

import (
	"context"
	"errors"
	"maps"
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
		s.recordMemoryFile(env, scope, file, false)
	}
	return out, err
}

func (s *Session) execMemoryWrite(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "write", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any) (any, error) {
		return execFileWrite(ctx, env, forwarded, s.fileReadGuard(env))
	})
}

func (s *Session) execMemoryRead(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", "read")
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := execFileRead(ctx, env, forwarded, s.fileReadGuard(env))
	// The index has its own baseline and change blocks; every other page
	// read is tracked for change notices.
	if err == nil && file != memoryIndexFile {
		s.recordMemoryFile(env, scope, file, true)
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
