package chatcompletions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/jpeg"
	"net/http"
	"strings"

	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/internal/imagerepair"
	"primeradiant.com/evener/llm/providers/internal/protocolhttp"
	"primeradiant.com/evener/llm/registry"
)

const unmanagedImageCaveat = "This image was recovered without its color profile. Its colors may be inaccurate; do not rely on exact color fidelity."

var errImageRepairUnchanged = errors.New("image repair produced no distinct usable request")

type imageRecovery struct {
	stage          int
	originalSize   int
	lastSize       int
	requireSmaller bool
	managed        bool
	warnings       []llm.Warning
	sent           map[[sha256.Size]byte]bool
}

// runImageRecovery owns only definite HTTP rejections. Once Stream returns a
// stream, its decoder and the session retain ownership of any later failures.
func runImageRecovery[T any](ctx context.Context, p *Protocol, req llm.Request, res registry.Resolved, stream bool, send func(*protocolhttp.Call, *imageRecovery) (T, error)) (T, error) {
	var zero T
	recovery := &imageRecovery{sent: make(map[[sha256.Size]byte]bool)}
	var originalErr, lastErr error
	for {
		if err := ctx.Err(); recovery.stage > 0 && err != nil {
			return zero, err
		}
		body, err := buildBody(req, res, stream)
		if err != nil {
			return zero, err
		}
		operation, endpoint := "chat.completions", res.Transport.Endpoint
		if stream {
			operation, endpoint = "chat.completions(stream)", res.Transport.StreamEndpoint
		}
		call := p.completionCall(operation, http.MethodPost, protocolhttp.URL(res, endpoint), body, req, res)
		finalize := call.FinalizeBody
		call.FinalizeBody = func(attemptCtx context.Context, body map[string]any) error {
			if err := finalize(attemptCtx, body); err != nil {
				return err
			}
			return llm.WrapContextError(res.Instance, recovery.prepare(attemptCtx, body))
		}
		result, err := send(call, recovery)
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if errors.Is(err, errImageRepairUnchanged) {
			if recovery.requireSmaller {
				return zero, originalErr
			}
			return zero, lastErr
		}
		expanded := recovery.stage == 1 && recovery.managed && recovery.lastSize > recovery.originalSize && providerStatus(err) == http.StatusRequestEntityTooLarge
		if recovery.stage == 2 || (!llm.IsImageNormalizationError(err) && !expanded) {
			return zero, err
		}
		if recovery.stage == 0 {
			originalErr = err
		}
		lastErr = err
		recovery.requireSmaller = expanded
		recovery.stage++
	}
}

func providerStatus(err error) int {
	if providerErr, ok := errors.AsType[llm.Error](err); ok {
		return providerErr.StatusCode()
	}
	return 0
}

func (r *imageRecovery) prepare(ctx context.Context, body map[string]any) error {
	r.managed = false
	r.warnings = nil
	if r.stage > 0 {
		mode := imagerepair.PreferColorManaged
		if r.stage == 2 {
			mode = imagerepair.ProfileFree
		}
		changed, unmanaged, managed, err := repairMessages(ctx, body, mode)
		if err != nil {
			return err
		}
		if !changed {
			return errImageRepairUnchanged
		}
		r.managed = managed
		if unmanaged {
			r.warnings = []llm.Warning{{Code: llm.WarningCodeImageColorUnmanaged, Message: unmanagedImageCaveat}}
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(encoded)
	if r.stage == 0 {
		r.originalSize = len(encoded)
	} else if r.sent[digest] || (r.requireSmaller && len(encoded) >= r.lastSize) {
		return errImageRepairUnchanged
	}
	r.sent[digest] = true
	r.lastSize = len(encoded)
	if r.stage > 0 {
		return ctx.Err()
	}
	return nil
}

// repairMessages copies the final wire subtree, including typed overlays and
// RawMessage values. UseNumber preserves unrelated extension integers exactly.
func repairMessages(ctx context.Context, body map[string]any, mode imagerepair.Mode) (changed, unmanaged, managed bool, err error) {
	raw, err := json.Marshal(body["messages"])
	if err != nil {
		return false, false, false, errImageRepairUnchanged
	}
	var messages []map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&messages); err != nil {
		return false, false, false, errImageRepairUnchanged
	}
	pixelsLeft := int64(32_000_000)
	for _, message := range messages {
		if err := ctx.Err(); err != nil {
			return false, false, false, err
		}
		parts, ok := message["content"].([]any)
		if !ok {
			continue
		}
		var repaired []any
		for i, value := range parts {
			part, _ := value.(map[string]any)
			imageURL, _ := part["image_url"].(map[string]any)
			uri, _ := imageURL["url"].(string)
			if part["type"] != "image_url" || imageURL == nil {
				if repaired != nil {
					repaired = append(repaired, value)
				}
				continue
			}
			result, err := repairImageURI(ctx, uri, mode, &pixelsLeft)
			if err != nil {
				return false, false, false, err
			}
			if result.Method != imagerepair.Unchanged {
				imageURL["url"] = llm.DataURI(result.MediaType, result.Data)
				if result.Method == imagerepair.ProfileRemoved {
					unmanaged = true
					if repaired == nil {
						repaired = append(make([]any, 0, len(parts)+1), parts[:i]...)
					}
					repaired = append(repaired, map[string]any{"type": "text", "text": unmanagedImageCaveat})
				} else {
					managed = true
				}
			}
			if repaired != nil {
				repaired = append(repaired, value)
			}
		}
		if repaired != nil {
			message["content"] = repaired
		}
	}
	changed = unmanaged || managed
	if changed {
		body["messages"] = messages
	}
	return changed, unmanaged, managed, nil
}

func repairImageURI(ctx context.Context, uri string, mode imagerepair.Mode, pixelsLeft *int64) (imagerepair.Result, error) {
	header, encoded, ok := strings.Cut(uri, ",")
	if !ok || (!strings.EqualFold(header, "data:image/jpeg;base64") && !strings.EqualFold(header, "data:image/jpg;base64")) {
		return imagerepair.Result{}, nil
	}
	// Bound base64 allocation before decoding. This is a repair limit only;
	// the ordinary request already sent the user's bytes unchanged.
	if len(encoded) > base64.StdEncoding.EncodedLen(32<<20) {
		return imagerepair.Result{}, nil
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return imagerepair.Result{}, nil //nolint:nilerr // Invalid base64 disables repair, preserving the provider's original error.
	}
	if mode == imagerepair.PreferColorManaged {
		if config, err := jpeg.DecodeConfig(bytes.NewReader(data)); err == nil {
			pixels := int64(config.Width) * int64(config.Height)
			if pixels > *pixelsLeft {
				mode = imagerepair.ProfileFree
			} else {
				*pixelsLeft -= pixels
			}
		}
	}
	return imagerepair.RepairJPEG(ctx, data, mode)
}
