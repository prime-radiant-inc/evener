package agent

import (
	"context"
	"fmt"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
)

func registerFileTools(reg *tool.Registry, deps *toolDeps) error {
	register := reg.Register
	if deps != nil && deps.registerTool != nil {
		register = func(registered tool.RegisteredTool) error {
			return deps.registerTool(reg, registered)
		}
	}
	// read_file
	if err := register(tool.RegisteredTool{
		Definition: tool.DefReadFile(), ReadOnly: true,
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return execFileRead(ctx, env, args, deps.readGuard)
		},
	}); err != nil {
		return err
	}

	// write_file
	if err := register(tool.RegisteredTool{
		Definition: tool.DefWriteFile(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return execFileWrite(ctx, env, args, deps.readGuard)
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = register(tool.RegisteredTool{
		Definition: tool.DefEditFile(),
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return execFileEdit(ctx, env, args, deps.readGuard)
		},
	})

	return nil
}

func execFileRead(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any, guard readGuard) (any, error) {
	_ = ctx
	return execFileReadWith(args, guard, env.ReadFile)
}

// execFileReadWith is read_file's semantics over read, so a caller can supply
// a reader that also captures what it loaded.
func execFileReadWith(args map[string]any, guard readGuard, read func(path string, offset, limit *int) (string, error)) (any, error) {
	path := fmt.Sprint(args["file_path"])
	offset := optionalIntArg(args, "offset")
	limit := optionalIntArg(args, "limit")
	visionAsk, _ := args["vision_prompt"].(string)
	result, err := read(path, offset, limit)
	if err == nil {
		guard.TrackRead(path)
		// If the file is an image or document (PDF), return an
		// tool.ImageResult so the vision side-channel can process it.
		if img := tool.ParseImageResult(path, result); img != nil {
			img.Prompt = visionAsk
			return *img, nil
		}
		if doc := tool.ParseDocumentResult(path, result); doc != nil {
			doc.Prompt = visionAsk
			return *doc, nil
		}
	}
	return result, err
}

func execFileWrite(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any, guard readGuard) (any, error) {
	_ = ctx
	path := fmt.Sprint(args["file_path"])
	warn := guard.ReadBeforeWriteWarning(path)
	result, err := env.WriteFile(path, fmt.Sprint(args["content"]))
	if err == nil {
		guard.TrackRead(path)
		if warn != "" {
			return warn + result, nil
		}
	}
	return result, err
}

func execFileEdit(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any, guard readGuard) (any, error) {
	_ = ctx
	return execFileEditWith(args, guard, env.EditFile)
}

// execFileEditWith is edit_file's semantics over edit, so a caller can supply
// an editor that also changes the bytes it writes.
func execFileEditWith(args map[string]any, guard readGuard, edit func(path, oldString, newString string, replaceAll bool) (string, error)) (any, error) {
	path := fmt.Sprint(args["file_path"])
	replaceAll := false
	if v, ok := args["replace_all"].(bool); ok {
		replaceAll = v
	}
	warn := guard.ReadBeforeWriteWarning(path)
	result, err := edit(path, fmt.Sprint(args["old_string"]), fmt.Sprint(args["new_string"]), replaceAll)
	if err == nil && warn != "" {
		return warn + result, nil
	}
	return result, err
}
