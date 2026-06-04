package agent

import (
	"context"
	"fmt"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func registerFileTools(reg *tool.Registry, deps *toolDeps) error {
	// read_file
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefReadFile(), ReadOnly: true},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			offset := optionalIntArg(args, "offset")
			limit := optionalIntArg(args, "limit")
			purpose, _ := args["purpose"].(string)
			result, err := env.ReadFile(path, offset, limit)
			if err == nil {
				deps.readGuard.TrackRead(path)
				// If the file is an image or document (PDF), return an
				// tool.ImageResult so the vision side-channel can process it.
				if img := tool.ParseImageResult(path, result); img != nil {
					img.Purpose = purpose
					return *img, nil
				}
				if doc := tool.ParseDocumentResult(path, result); doc != nil {
					doc.Purpose = purpose
					return *doc, nil
				}
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// write_file
	if err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefWriteFile()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.WriteFile(path, fmt.Sprint(args["content"]))
			if err == nil && warn != "" {
				return warn + result, nil
			}
			return result, err
		},
	}); err != nil {
		return err
	}

	// edit_file
	_ = reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: tool.DefEditFile()},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			_ = ctx
			path := fmt.Sprint(args["file_path"])
			replaceAll := false
			if v, ok := args["replace_all"].(bool); ok {
				replaceAll = v
			}
			warn := deps.readGuard.ReadBeforeWriteWarning(path)
			result, err := env.EditFile(path, fmt.Sprint(args["old_string"]), fmt.Sprint(args["new_string"]), replaceAll)
			if err == nil && warn != "" {
				return warn + result, nil
			}
			return result, err
		},
	})

	return nil
}
