package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/fspaths"
)

const outputImageMaxCandidates = 20
const outputImageMaxRendered = 8
const outputImageMaxBytes = 8 * 1024 * 1024

var outputImageExtRegexp = regexp.MustCompile(`(?i)(?:"([^"]+\.(?:png|jpe?g|gif|webp))"|'([^']+\.(?:png|jpe?g|gif|webp))'|([^\s"']+\.(?:png|jpe?g|gif|webp)))`)

func shellOutputImageCandidates(output string) []string {
	matches := outputImageExtRegexp.FindAllStringSubmatch(output, -1)
	capHint := len(matches)
	if capHint > outputImageMaxCandidates {
		capHint = outputImageMaxCandidates
	}
	out := make([]string, 0, capHint)
	seen := map[string]struct{}{}
	for _, m := range matches {
		cand := ""
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				cand = strings.TrimSpace(m[i])
				break
			}
		}
		lower := strings.ToLower(cand)
		if cand == "" || strings.Contains(lower, "://") {
			continue
		}
		if _, ok := seen[cand]; ok {
			continue
		}
		seen[cand] = struct{}{}
		out = append(out, cand)
		if len(out) >= outputImageMaxCandidates {
			break
		}
	}
	return out
}

func supportedOutputImageMedia(data []byte, name string) (string, bool) {
	if len(data) == 0 {
		return "", false
	}
	ct := http.DetectContentType(data)
	switch ct {
	case "image/png", "image/jpeg", "image/gif", "image/webp":
		return ct, true
	}
	// http.DetectContentType can return octet-stream for small WebP samples; allow
	// only when the RIFF/WEBP signature is present.
	if len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")) {
		return "image/webp", true
	}
	return "", false
}

func resolveOutputImageFile(sessionID, cwd, candidate, source string) (appwire.OutputImage, bool) {
	abs, err := fspaths.ResolveInRoot(cwd, candidate)
	if err != nil {
		return appwire.OutputImage{}, false
	}
	data, info, ok := readOutputImageFile(abs)
	if !ok {
		return appwire.OutputImage{}, false
	}
	mediaType, ok := supportedOutputImageMedia(data, filepath.Base(abs))
	if !ok {
		return appwire.OutputImage{}, false
	}
	realRoot, err := filepath.EvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return appwire.OutputImage{}, false
	}
	rel, err := filepath.Rel(realRoot, abs)
	if err != nil {
		return appwire.OutputImage{}, false
	}
	rel = filepath.ToSlash(rel)
	return appwire.OutputImage{
		Source:    source,
		Name:      outputImageDisplayName(abs),
		MediaType: mediaType,
		Size:      info.Size(),
		URL:       "/doc/image?session=" + url.QueryEscape(sessionID) + "&path=" + url.QueryEscape(rel),
		SHA:       outputImageSHA(data),
		Path:      rel,
	}, true
}

func readOutputImageFile(abs string) ([]byte, os.FileInfo, bool) {
	info, err := os.Stat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > outputImageMaxBytes {
		return nil, nil, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, nil, false
	}
	return data, info, true
}

func outputImageSHA(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func outputImageDisplayName(path string) string {
	if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
		return base
	}
	return "image"
}
