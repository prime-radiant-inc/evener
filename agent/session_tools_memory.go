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
	env, release, err := s.acquireMemoryEnvironment(stringArg(args, "scope"))
	if err != nil {
		return nil, nil, nil, err
	}
	path := stringArg(args, key)
	if key == "path" && path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) {
		release()
		return nil, nil, nil, errors.New("memory path must be relative and remain in its scope")
	}
	forwarded := maps.Clone(args)
	forwarded[key] = filepath.Join(env.WorkingDirectory(), path)
	if err := s.beforeMemoryIO(stringArg(args, "scope"), operation); err != nil {
		release()
		return nil, nil, nil, err
	}
	return env, forwarded, release, nil
}

func (s *Session) execMemoryWrite(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, args, release, err := s.memoryFileArgs(args, "file_path", "write")
	if err != nil {
		return nil, err
	}
	defer release()
	return execFileWrite(ctx, env, args, s.fileReadGuard(env))
}

func (s *Session) execMemoryRead(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, args, release, err := s.memoryFileArgs(args, "file_path", "read")
	if err != nil {
		return nil, err
	}
	defer release()
	return execFileRead(ctx, env, args, s.fileReadGuard(env))
}
func (s *Session) execMemoryEdit(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, args, release, err := s.memoryFileArgs(args, "file_path", "edit")
	if err != nil {
		return nil, err
	}
	defer release()
	return execFileEdit(ctx, env, args, s.fileReadGuard(env))
}
func (s *Session) execMemorySearch(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, args, release, err := s.memoryFileArgs(args, "path", "search")
	if err != nil {
		return nil, err
	}
	defer release()
	return execFileGrep(ctx, env, args)
}
func (s *Session) execMemoryDelete(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, args, release, err := s.memoryFileArgs(args, "file_path", "delete")
	if err != nil {
		return nil, err
	}
	defer release()
	path := stringArg(args, "file_path")
	warn := s.fileReadGuard(env).ReadBeforeWriteWarning(path)
	if err := env.RemoveConfinedFile(path); err != nil {
		return nil, err
	}
	return warn + "Removed or already absent: " + path, nil
}
