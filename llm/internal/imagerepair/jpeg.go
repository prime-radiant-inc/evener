// Package imagerepair builds disposable, bounded provider image repairs.
// It never modifies its input. Unsupported inputs remain available to the caller.
package imagerepair

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
)

// Mode chooses color conversion or lossless removal of ICC metadata only.
type Mode uint8

const (
	PreferColorManaged Mode = iota
	ProfileFree
)

// Method identifies what changed, so callers can disclose unmanaged recovery.
type Method uint8

const (
	Unchanged Method = iota
	ColorManaged
	ProfileRemoved
)

// Result is a disposable repair. Unchanged Data may alias the input.
type Result struct {
	Data      []byte
	MediaType string
	Method    Method
}

const (
	maxJPEG    = 32 << 20
	maxPixels  = 16_000_000
	maxICC     = 1 << 20
	maxPNG     = 64 << 20
	maxMarkers = 65536
)

// RepairJPEG converts a single JPEG to opaque sRGB PNG, applying EXIF orientation.
// Unsupported conversion falls back to removing ICC APP2 segments only. Neither
// path changes multipicture images or structurally unsafe JPEGs. Only cancellation
// is an error: exceeding a recovery budget never rejects the original attachment.
func RepairJPEG(ctx context.Context, data []byte, mode Mode) (Result, error) {
	original := Result{Data: data, MediaType: "image/jpeg", Method: Unchanged}
	if err := ctx.Err(); err != nil {
		return original, err
	}
	if len(data) > maxJPEG || (mode != PreferColorManaged && mode != ProfileFree) {
		return original, nil
	}
	meta, ok := scanJPEG(ctx, data)
	if err := ctx.Err(); err != nil {
		return original, err
	}
	if !ok || meta.multipicture {
		return original, nil
	}
	if mode == PreferColorManaged {
		if out, ok := convertJPEG(ctx, data, meta); ok {
			if err := ctx.Err(); err != nil {
				return original, err
			}
			return Result{Data: out, MediaType: "image/png", Method: ColorManaged}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return original, err
	}
	if len(meta.icc) == 0 {
		return original, nil
	}
	// Keep every byte outside ICC marker ranges, including entropy, EXIF and APP2.
	out := make([]byte, 0, len(data)-meta.iccBytes)
	pos := 0
	for _, r := range meta.icc {
		var err error
		out, err = appendContext(ctx, out, data[pos:r.start])
		if err != nil {
			return original, err
		}
		pos = r.end
	}
	out, err := appendContext(ctx, out, data[pos:])
	if err != nil {
		return original, err
	}
	return Result{Data: out, MediaType: "image/jpeg", Method: ProfileRemoved}, nil
}

type byteRange struct{ start, end int }
type jpegMetadata struct {
	icc          []byteRange
	iccBytes     int
	exif         [][]byte
	multipicture bool
}

// scanJPEG walks every scan through EOI, not only the metadata before the first
// SOS. Trailing bytes are refused, so a concatenated JPEG cannot lose a frame.
func scanJPEG(ctx context.Context, b []byte) (jpegMetadata, bool) {
	var m jpegMetadata
	if len(b) < 4 || b[0] != 255 || b[1] != 0xd8 {
		return m, false
	}
	pos, markers := 2, 0
	inScan, frame, scan := false, false, false
	for pos < len(b) {
		if err := ctx.Err(); err != nil {
			return m, false
		}
		if inScan {
			for pos < len(b) && b[pos] != 255 {
				pos++
				if pos%4096 == 0 && ctx.Err() != nil {
					return m, false
				}
			}
		}
		if pos >= len(b) || b[pos] != 255 {
			return m, false
		}
		// Preserve marker fill bytes even when removing the following ICC segment.
		for pos < len(b) && b[pos] == 255 {
			pos++
			if pos%4096 == 0 && ctx.Err() != nil {
				return m, false
			}
		}
		if pos >= len(b) {
			return m, false
		}
		start, code := pos-1, b[pos]
		pos++
		if inScan && (code == 0 || code >= 0xd0 && code <= 0xd7) {
			continue
		}
		inScan = false
		markers++
		if markers > maxMarkers {
			return m, false
		}
		if code == 0xd9 {
			return m, frame && scan && pos == len(b)
		}
		if code == 0 || code == 0xd8 || code == 1 || code >= 0xd0 && code <= 0xd7 {
			return m, false
		}
		if pos+2 > len(b) {
			return m, false
		}
		size := int(binary.BigEndian.Uint16(b[pos:]))
		if size < 2 || size > len(b)-pos {
			return m, false
		}
		end := pos + size
		body := b[pos+2 : end]
		switch code {
		case 0xe2:
			if bytes.HasPrefix(body, []byte("MPF\x00")) {
				m.multipicture = true
			}
			if bytes.HasPrefix(body, []byte("ICC_PROFILE\x00")) {
				m.icc = append(m.icc, byteRange{start, end})
				m.iccBytes += end - start
			}
		case 0xe1:
			if bytes.HasPrefix(body, []byte("Exif\x00\x00")) {
				m.exif = append(m.exif, body[6:])
			}
		case 0xda:
			if !frame {
				return m, false
			}
			scan = true
			inScan = true
		default:
			// All SOF families, including unsupported codecs, must contain only one frame.
			if code >= 0xc0 && code <= 0xcf && code != 0xc4 && code != 0xc8 && code != 0xcc {
				if frame || len(body) < 6 {
					return m, false
				}
				frame = true
			}
		}
		pos = end
	}
	return m, false
}

func (m jpegMetadata) profile(ctx context.Context, data []byte) ([]byte, bool) {
	if len(m.icc) == 0 {
		return nil, true
	}
	var chunks [255][]byte
	total, size := 0, 0
	for _, r := range m.icc {
		if ctx.Err() != nil {
			return nil, false
		}
		body := data[r.start+4 : r.end]
		if len(body) < 14 {
			return nil, false
		}
		seq, count := int(body[12]), int(body[13])
		if seq == 0 || count == 0 || seq > count || total != 0 && total != count || chunks[seq-1] != nil {
			return nil, false
		}
		total = count
		chunks[seq-1] = body[14:]
		size += len(body) - 14
		if size > maxICC {
			return nil, false
		}
	}
	if len(m.icc) != total {
		return nil, false
	}
	profile := make([]byte, 0, size)
	for _, chunk := range chunks[:total] {
		if chunk == nil {
			return nil, false
		}
		var err error
		profile, err = appendContext(ctx, profile, chunk)
		if err != nil {
			return nil, false
		}
	}
	return profile, true
}

func convertJPEG(ctx context.Context, data []byte, m jpegMetadata) ([]byte, bool) {
	orientation := 1
	if len(m.exif) > 1 {
		return nil, false
	} // Ambiguous EXIF must not silently choose a rotation.
	if len(m.exif) == 1 {
		var ok bool
		orientation, ok = parseOrientation(m.exif[0])
		if !ok {
			return nil, false
		}
	}
	profile, ok := m.profile(ctx, data)
	if !ok {
		return nil, false
	}
	var transform *iccTransform
	if len(m.icc) != 0 {
		transform, ok = parseICC(ctx, profile)
		if !ok {
			return nil, false
		}
	}
	config, err := jpeg.DecodeConfig(&contextReader{ctx: ctx, r: bytes.NewReader(data)})
	if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > maxPixels/config.Height {
		return nil, false
	}
	switch config.ColorModel {
	case color.YCbCrModel, color.RGBAModel:
	case color.GrayModel:
		if transform != nil {
			return nil, false
		}
	default:
		return nil, false // In particular, never interpret CMYK as sRGB.
	}
	src, err := jpeg.Decode(&contextReader{ctx: ctx, r: bytes.NewReader(data)})
	if err != nil || ctx.Err() != nil {
		return nil, false
	}
	w, h := config.Width, config.Height
	ow, oh := w, h
	if orientation >= 5 {
		ow, oh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := range h {
		for x := range w {
			if x%1024 == 0 && ctx.Err() != nil {
				return nil, false
			}
			r, g, b, _ := src.At(x, y).RGBA()
			c := color.RGBA{R: uint8(r >> 8), G: uint8(g >> 8), B: uint8(b >> 8), A: 255}
			if transform != nil {
				c = transform.rgb(c.R, c.G, c.B)
			}
			c.A = 255
			dx, dy := orientPoint(x, y, w, h, orientation)
			out.SetRGBA(dx, dy, c)
		}
	}
	writer := &pngWriter{ctx: ctx}
	if err := png.Encode(writer, out); err != nil || ctx.Err() != nil {
		return nil, false
	}
	return writer.buf.Bytes(), true
}

func appendContext(ctx context.Context, dst, src []byte) ([]byte, error) {
	for len(src) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n := min(len(src), 32768)
		dst = append(dst, src[:n]...)
		src = src[n:]
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return dst, nil
}

// Standard library codecs check cancellation at bounded I/O boundaries. Pixel
// conversion checks separately; no background decoder can outlive this call.
type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p[:min(len(p), 4096)])
}

var errPNGBudget = errors.New("PNG repair exceeds output budget")

type pngWriter struct {
	ctx context.Context
	buf bytes.Buffer
}

func (w *pngWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) > maxPNG-w.buf.Len() {
		return 0, errPNGBudget
	}
	return w.buf.Write(p)
}
