package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/pdfslice"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// errPagesOnNonPDF is read_file's refusal for a pages selector on a text or
// image file. It is returned before guard.TrackRead, so a refused read never
// satisfies read-before-write.
var errPagesOnNonPDF = errors.New("read_file: pages parameter is only supported for PDF files")

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
	if rr, ok := env.(execenv.RichFileReader); ok {
		return execFileReadWith(ctx, args, guard, func(path string, offset, limit *int) (string, execenv.ReadFileMeta, error) {
			return rr.ReadFileRichCtx(ctx, path, offset, limit)
		})
	}
	if ce, ok := env.(execenv.ContextualFileReader); ok {
		return execFileReadWith(ctx, args, guard, func(path string, offset, limit *int) (string, execenv.ReadFileMeta, error) {
			text, err := ce.ReadFileCtx(ctx, path, offset, limit)
			return text, execenv.ReadFileMeta{}, err
		})
	}
	return execFileReadWith(ctx, args, guard, func(path string, offset, limit *int) (string, execenv.ReadFileMeta, error) {
		text, err := env.ReadFile(path, offset, limit)
		return text, execenv.ReadFileMeta{}, err
	})
}

// execFileReadWith is read_file's semantics over read, so a caller can supply
// a reader that also captures what it loaded and reports the structural facts
// of a text read. ctx is threaded for the media work (image downscaling, PDF
// page slicing) that runs on the result here; the read itself already carries
// the caller's context.
func execFileReadWith(ctx context.Context, args map[string]any, guard readGuard, read func(path string, offset, limit *int) (string, execenv.ReadFileMeta, error)) (any, error) {
	path := fmt.Sprint(args["file_path"])
	offset := optionalIntArg(args, "offset")
	limit := optionalIntArg(args, "limit")
	visionAsk, _ := args["vision_prompt"].(string)
	pages, _ := args["pages"].(string)
	hasPages := strings.TrimSpace(pages) != ""
	result, meta, err := read(path, offset, limit)
	if err == nil {
		// If the file is an image or document (PDF), return an
		// tool.ImageResult so the vision side-channel can process it.
		if img := tool.ParseImageResult(path, result); img != nil {
			if hasPages {
				return "", errPagesOnNonPDF
			}
			guard.TrackRead(path)
			prepared, err := llm.PrepareImageForModel(img.Data)
			if err != nil {
				// Keep the malformed-raster wording the registry's own
				// post-decode check produces, so a truncated image fails
				// here exactly as it did after dispatch.
				return "", fmt.Errorf("invalid image data: %w", err)
			}
			img.Data = prepared.Data
			img.MediaType = prepared.MediaType
			if prepared.Scaled {
				scale := max(float64(prepared.OriginalWidth)/float64(prepared.Width),
					float64(prepared.OriginalHeight)/float64(prepared.Height))
				img.Text += fmt.Sprintf("\n[Image: original %dx%d, displayed at %dx%d (multiply coordinates by %.2f to map to original)]",
					prepared.OriginalWidth, prepared.OriginalHeight, prepared.Width, prepared.Height, scale)
			}
			img.Prompt = visionAsk
			return *img, nil
		}
		if doc := tool.ParseDocumentResult(path, result); doc != nil {
			sliced, from, to, totalPages, err := pdfslice.SlicePDF(ctx, doc.Data, pages)
			if err != nil {
				return "", err
			}
			guard.TrackRead(path)
			doc.Data = sliced
			doc.ReadPages = fmt.Sprintf("%d-%d", from, to)
			doc.Text = fmt.Sprintf("[document: pdf, pages %d-%d of %d total, %d bytes, base64 data follows]",
				from, to, totalPages, len(sliced))
			doc.Prompt = visionAsk
			return *doc, nil
		}
		if hasPages {
			return "", errPagesOnNonPDF
		}
		guard.TrackRead(path)
		text := tool.TextResult{
			Output:             result,
			FullOutput:         result,
			ReadStartLine:      meta.StartLine,
			ReadEndLine:        meta.EndLine,
			ReadPastEOF:        meta.PastEOF,
			ReadUnnumberedTail: meta.TailMode && meta.StartLine == 0 && meta.EndLine == 0,
			ReadClamped:        meta.ClampedLines > 0,
		}
		return text, nil
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
	return execFileEditWith(args, guard, func(path string, edits []execenv.FileEdit) (string, error) {
		if be, ok := env.(execenv.BatchFileEditor); ok {
			return be.EditFileBatchCtx(ctx, path, edits)
		}
		if len(edits) > 1 {
			return "", errors.New("edit_file: execution environment does not support multi-edit")
		}
		e := edits[0]
		if ce, ok := env.(execenv.ContextualFileReader); ok {
			return ce.EditFileCtx(ctx, path, e.OldString, e.NewString, e.ReplaceAll)
		}
		return env.EditFile(path, e.OldString, e.NewString, e.ReplaceAll)
	})
}

// execFileEditWith is edit_file's semantics over edit, so a caller can supply
// an editor that also changes the bytes it writes.
func execFileEditWith(args map[string]any, guard readGuard, edit func(path string, edits []execenv.FileEdit) (string, error)) (any, error) {
	path, ok := args["file_path"].(string)
	if !ok {
		return nil, errors.New("edit_file: file_path must be a string")
	}
	edits, err := parseFileEdits(args)
	if err != nil {
		return nil, err
	}
	warn := guard.ReadBeforeWriteWarning(path)
	result, err := edit(path, edits)
	if err == nil && warn != "" {
		return warn + result, nil
	}
	return result, err
}

func parseFileEdits(args map[string]any) ([]execenv.FileEdit, error) {
	rawEdits, exists := args["edits"]
	if !exists || rawEdits == nil {
		return nil, errors.New("edit_file: edits is required")
	}
	items, ok := rawEdits.([]any)
	if !ok {
		return nil, errors.New("edit_file: edits must be an array")
	}
	if len(items) == 0 {
		return nil, errors.New("edit_file: edits must not be empty")
	}
	edits := make([]execenv.FileEdit, len(items))
	for i, item := range items {
		m, ok := item.(map[string]any)
		if !ok || m == nil {
			return nil, fmt.Errorf("edit_file: edits[%d] must be an object", i)
		}
		oldStr, ok := m["old_string"].(string)
		if !ok {
			return nil, fmt.Errorf("edit_file: edits[%d].old_string must be a string", i)
		}
		newStr, ok := m["new_string"].(string)
		if !ok {
			return nil, fmt.Errorf("edit_file: edits[%d].new_string must be a string", i)
		}
		var replaceAll bool
		if rawAll, hasAll := m["replace_all"]; hasAll && rawAll != nil {
			b, ok := rawAll.(bool)
			if !ok {
				return nil, fmt.Errorf("edit_file: edits[%d].replace_all must be a boolean", i)
			}
			replaceAll = b
		}
		edits[i] = execenv.FileEdit{
			OldString:  oldStr,
			NewString:  newStr,
			ReplaceAll: replaceAll,
		}
	}
	return edits, nil
}
