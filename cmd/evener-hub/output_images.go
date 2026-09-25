package hub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/fspaths"
)

var outputImageMarshal = json.Marshal
var outputImageResolve = resolveOutputImageFile
var outputImageStat = os.Stat
var outputImageReadFile = os.ReadFile
var outputImageEvalSymlinks = filepath.EvalSymlinks
var outputImageRel = filepath.Rel

const outputImageMaxCandidates = 20
const outputImageMaxRendered = 8
const outputImageMaxBytes = 8 * 1024 * 1024

var outputImageExtRegexp = regexp.MustCompile(`(?i)(?:"([^"]+\.(?:png|jpe?g|gif|webp))"|'([^']+\.(?:png|jpe?g|gif|webp))'|([^\s"']+\.(?:png|jpe?g|gif|webp)))`)

func outputImagesForToolCall(sessionID, cwd, toolName, argumentsJSON, output string) []appwire.OutputImage {
	type candidate struct {
		path   string
		source string
	}
	var candidates []candidate
	var args map[string]any
	_ = json.Unmarshal([]byte(argumentsJSON), &args)
	addArgPath := func(key, source string) {
		if v, ok := args[key].(string); ok && strings.TrimSpace(v) != "" {
			candidates = append(candidates, candidate{path: v, source: source})
		}
	}
	switch toolName {
	case "write_file", "edit_file":
		addArgPath("file_path", "written-file")
		addArgPath("path", "written-file")
	case "read_file":
		// read_file's own file_path/path arg names the file it read, same
		// shape as write_file/edit_file's own arg above - a distinct source
		// tag ("read-file", not "written-file") since this file wasn't
		// written by the call. This is the file-backed, re-read-from-disk
		// path: /doc/image, already live-session-capable (unlike the
		// sha-addressed /s/.../images/ route a past thread read separately
		// attaches via projectReplayOutputImages for the same call, source
		// "tool-result" - appendOutputImagesUnique's sha-first key, below,
		// is what keeps those two from showing the same image twice when
		// both are present on a past read (kata 1nr4).
		addArgPath("file_path", "read-file")
		addArgPath("path", "read-file")
	case "apply_patch":
		for _, p := range shellOutputImageCandidates(output) {
			candidates = append(candidates, candidate{path: p, source: "written-file"})
		}
	case "shell", "exec_command":
		for _, p := range shellOutputImageCandidates(output) {
			candidates = append(candidates, candidate{path: p, source: "shell-path"})
		}
	}
	out := make([]appwire.OutputImage, 0, min(len(candidates), outputImageMaxRendered))
	seen := map[string]struct{}{}
	for _, c := range candidates {
		img, ok := outputImageResolve(sessionID, cwd, c.path, c.source)
		if !ok || img.URL == "" {
			continue
		}
		if _, exists := seen[img.URL]; exists {
			continue
		}
		seen[img.URL] = struct{}{}
		out = append(out, img)
		if len(out) >= outputImageMaxRendered {
			break
		}
	}
	return out
}

// enrichOutputImageNotification completes a live tool call's output images on
// their way to the browser, in the call items a history/updated carries and
// the tool item an overlay/upserted carries: it adds the file-backed
// descriptors this hub can resolve by re-reading the call's own file argument
// off disk, and it stamps the sha-addressed route onto whatever descriptors
// the daemon minted without one (see stampOutputImageURLs).
//
// Only the sha stamp works without a cwd, so a session whose working directory
// is unknown still gets its tool-result thumbnails.
func enrichOutputImageNotification(sessionID, cwd string, argsByCallID map[string]string, notification appwire.Notification) appwire.Notification {
	sessionID = strings.TrimSpace(sessionID)
	cwd = strings.TrimSpace(cwd)
	if sessionID == "" {
		return notification
	}
	enrich := func(item *appwire.ThreadItem) bool {
		return enrichOutputImageItem(sessionID, cwd, argsByCallID, item)
	}
	switch notification.Method {
	case appwire.NotifyHistoryUpdated:
		return rewriteNotificationField(notification, "items", func(items *[]appwire.ThreadItem) bool {
			changed := false
			for i := range *items {
				changed = enrich(&(*items)[i]) || changed
			}
			return changed
		})
	case appwire.NotifyOverlayUpserted:
		return rewriteNotificationField(notification, "item", func(item *appwire.OverlayItem) bool {
			return enrich(&item.Item)
		})
	}
	return notification
}

// rewriteNotificationField decodes one field of notification's params,
// lets edit change it, and re-encodes the params only when edit reports a
// change, so a frame the hub has nothing to add passes through byte for byte.
func rewriteNotificationField[T any](notification appwire.Notification, field string, edit func(*T) bool) appwire.Notification {
	var params map[string]json.RawMessage
	if len(notification.Params) == 0 || json.Unmarshal(notification.Params, &params) != nil {
		return notification
	}
	var value T
	if raw := params[field]; len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return notification
	}
	if !edit(&value) {
		return notification
	}
	encoded, err := outputImageMarshal(value)
	if err != nil {
		return notification
	}
	params[field] = encoded
	data, err := outputImageMarshal(params)
	if err != nil {
		return notification
	}
	notification.Params = data
	return notification
}

// enrichOutputImageItem completes one tool call item's output images once the
// call has finished, remembering a running call's arguments for when it does.
// It reports whether it changed the item.
func enrichOutputImageItem(sessionID, cwd string, argsByCallID map[string]string, item *appwire.ThreadItem) bool {
	if item.Type != "commandExecution" {
		return false
	}
	if argsByCallID != nil && item.CallID != "" && item.ArgumentsJSON != "" {
		argsByCallID[item.CallID] = item.ArgumentsJSON
	}
	if item.Status == appwire.TurnStatusInProgress {
		return false
	}
	var fileBacked []appwire.OutputImage
	if cwd != "" {
		argsJSON := item.ArgumentsJSON
		if argsJSON == "" && item.CallID != "" {
			argsJSON = argsByCallID[item.CallID]
		}
		fileBacked = outputImagesForToolCall(sessionID, cwd, item.ToolName, argsJSON, item.Output)
	}
	if argsByCallID != nil && item.CallID != "" {
		delete(argsByCallID, item.CallID)
	}
	images := appendOutputImagesUnique(item.OutputImages, fileBacked)
	stamped := stampOutputImageURLs(sessionID, images)
	if len(fileBacked) == 0 && !stamped {
		return false
	}
	item.OutputImages = images
	return true
}

// stampOutputImageURLs fills in the route this hub serves sha-addressed image
// bytes on (handleSessionImage, web_workspace.go) for every descriptor that
// names its content by sha but carries no route to fetch it from. It reports
// whether it changed anything.
//
// Producers deliberately leave that URL empty: the agent minting a live
// descriptor (events.ToolResultOutputImage) and the transcript projector
// minting the reload counterpart (apptranscript.ToolResultOutputImages) both
// know the sha and neither knows the serving route. This is the one place that
// decision is made, for live turns and reloaded ones alike.
//
// A descriptor that already carries a URL is left alone: the file-backed
// mechanism above resolves its own /doc/image route for the same bytes, and
// that route serves a file this hub can re-read rather than a sha it has to
// scan the transcript for.
func stampOutputImageURLs(sessionID string, images []appwire.OutputImage) bool {
	if sessionID == "" {
		return false
	}
	stamped := false
	for i := range images {
		if images[i].URL == "" && images[i].SHA != "" {
			images[i].URL = sessionImageURL(sessionID, images[i].SHA)
			stamped = true
		}
	}
	return stamped
}

// stampSessionImageURLs is stampOutputImageURLs over every item of every turn,
// plus the replayed user-input images: apptranscript.AddressedImageProjector strips their
// bytes and records the sha in metadata, and handleSessionImage serves exactly
// that sha back, so the fetchable route belongs on the item itself (kata ck8z)
// rather than being left for the client to reconstruct from metadata.
func stampSessionImageURLs(sessionID string, turns []appwire.Turn) {
	if sessionID == "" {
		return
	}
	for ti := range turns {
		for ii := range turns[ti].Items {
			stampOutputImageURLs(sessionID, turns[ti].Items[ii].OutputImages)
			stampInputImageURLs(sessionID, turns[ti].Items[ii].Images)
		}
	}
}

// stampInputImageURLs gives each sha-bearing input image its sha route,
// leaving already-routed images and sha-less inline images alone.
func stampInputImageURLs(sessionID string, images []appwire.InputItem) {
	for i := range images {
		if images[i].URL != "" {
			continue
		}
		sha := strings.TrimSpace(images[i].Metadata["sha"])
		if sha == "" {
			continue
		}
		images[i].URL = sessionImageURL(sessionID, sha)
	}
}

// stampThreadImageURLs is stampSessionImageURLs over a whole thread, resolving
// the session the same way the file-backed enrichment does.
func stampThreadImageURLs(thread appwire.Thread) appwire.Thread {
	sessionID := strings.TrimSpace(thread.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(thread.ID)
	}
	stampSessionImageURLs(sessionID, thread.Turns)
	return thread
}

func sessionImageURL(sessionID, sha string) string {
	return "/s/" + url.PathEscape(sessionID) + "/images/" + sha
}

func shellOutputImageCandidates(output string) []string {
	matches := outputImageExtRegexp.FindAllStringSubmatch(output, -1)
	capHint := min(len(matches), outputImageMaxCandidates)
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

func supportedOutputImageMedia(data []byte, _ string) (string, bool) {
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
	realRoot, err := outputImageEvalSymlinks(filepath.Clean(cwd))
	if err != nil {
		return appwire.OutputImage{}, false
	}
	rel, err := outputImageRel(realRoot, abs)
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
		SHA:       imageSha(data),
		Path:      rel,
	}, true
}

func readOutputImageFile(abs string) ([]byte, os.FileInfo, bool) {
	info, err := outputImageStat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > outputImageMaxBytes {
		return nil, nil, false
	}
	data, err := outputImageReadFile(abs)
	if err != nil {
		return nil, nil, false
	}
	return data, info, true
}

func outputImageDisplayName(path string) string {
	if base := filepath.Base(path); base != "." && base != string(filepath.Separator) {
		return base
	}
	return "image"
}
