package hubcore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/spf13/afero"

	"primeradiant.com/evener/appwire"
)

const (
	transcriptDisplaySnapshotVersion uint64 = 1
	transcriptDisplayStateSubdir            = "transcript-display"
	transcriptDisplayStateFilename          = "state.json"
)

type transcriptDisplaySnapshot struct {
	Version uint64                           `json:"version"`
	Desktop appwire.TranscriptDisplayDefault `json:"desktop"`
	Mobile  appwire.TranscriptDisplayDefault `json:"mobile"`
}

type transcriptDisplayStoreFaults struct {
	BeforeRename func() error
	AfterRename  func() error
}

// TranscriptDisplayStore is the hub-authoritative store for the two
// transcript-display defaults. Desktop and Mobile revisions advance
// independently, while one mutex serializes each durable update.
type TranscriptDisplayStore struct {
	mu      sync.Mutex
	fs      afero.Fs
	root    string
	state   transcriptDisplaySnapshot
	loadErr error
	faults  transcriptDisplayStoreFaults
}

// NewTranscriptDisplayStore opens the transcript-display state below stateRoot.
// A malformed snapshot returns a usable shipped-default fallback together with
// the load diagnostic; callers must retain both values and report the error.
func NewTranscriptDisplayStore(stateRoot string) (*TranscriptDisplayStore, error) {
	return newTranscriptDisplayStoreFS(afero.NewOsFs(), stateRoot, transcriptDisplayStoreFaults{})
}

func newTranscriptDisplayStoreFS(fs afero.Fs, stateRoot string, faults transcriptDisplayStoreFaults) (*TranscriptDisplayStore, error) {
	state, err := loadTranscriptDisplaySnapshotFS(fs, stateRoot)
	if err != nil {
		state = transcriptDisplayShippedSnapshot()
	}
	return &TranscriptDisplayStore{
		fs:      fs,
		root:    stateRoot,
		state:   state,
		loadErr: err,
		faults:  faults,
	}, err
}

// Snapshot returns a deep value copy of the canonical Desktop and Mobile
// defaults. The returned custom-content pointer is never shared with the store.
func (s *TranscriptDisplayStore) Snapshot() appwire.TranscriptDisplayDefaults {
	if s == nil {
		return appwire.TranscriptDisplayShippedDefaults()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return transcriptDisplaySnapshotDefaults(s.state)
}

// Patch validates and durably updates one layout using optimistic revision
// checking. A durable error before rename leaves the old state published; an
// error after rename publishes the new canonical state in memory.
func (s *TranscriptDisplayStore) Patch(params appwire.TranscriptDisplayDefaultsPatchParams) (appwire.TranscriptDisplayPatchResponse, error) {
	if s == nil {
		return appwire.TranscriptDisplayPatchResponse{}, errors.New("transcript display store is not configured")
	}
	if !validTranscriptDisplayLayout(params.Layout) {
		return appwire.TranscriptDisplayPatchResponse{}, fmt.Errorf("invalid transcript display viewport class %q", params.Layout)
	}
	if err := appwire.ValidateTranscriptDisplayConfig(params.Config); err != nil {
		return appwire.TranscriptDisplayPatchResponse{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return appwire.TranscriptDisplayPatchResponse{}, fmt.Errorf("transcript display state is unavailable: %w", s.loadErr)
	}

	current := transcriptDisplaySnapshotLayout(s.state, params.Layout)
	if current.Revision != params.ExpectedRevision {
		return appwire.TranscriptDisplayPatchResponse{}, transcriptDisplayConflict(params.Layout, current, params.ExpectedRevision)
	}
	if reflect.DeepEqual(current.Config, params.Config) {
		return transcriptDisplayPatchResponse(params.Layout, current), nil
	}
	if current.Revision == math.MaxUint64 {
		return appwire.TranscriptDisplayPatchResponse{}, fmt.Errorf("transcript display %s revision overflow", params.Layout)
	}

	next := cloneTranscriptDisplaySnapshot(s.state)
	updated := transcriptDisplaySnapshotLayoutPtr(&next, params.Layout)
	updated.Revision++
	updated.Config = cloneTranscriptDisplayConfig(params.Config)
	renamed, err := saveTranscriptDisplaySnapshotFS(s.fs, s.root, next, s.faults)
	if renamed {
		s.state = next
	}
	if err != nil {
		return appwire.TranscriptDisplayPatchResponse{}, err
	}
	return transcriptDisplayPatchResponse(params.Layout, *updated), nil
}

func validTranscriptDisplayLayout(layout appwire.TranscriptViewportClass) bool {
	switch layout {
	case appwire.TranscriptViewportDesktop, appwire.TranscriptViewportMobile:
		return true
	default:
		return false
	}
}

func transcriptDisplayConflict(layout appwire.TranscriptViewportClass, current appwire.TranscriptDisplayDefault, expected uint64) appwire.WireError {
	return appwire.WireError{
		Code:    appwire.CodeConflict,
		Message: fmt.Sprintf("transcript display %s revision conflict: expected %d, current %d", layout, expected, current.Revision),
		Data: appwire.TranscriptDisplayConflictData{
			EvenerErrorInfo: appwire.ErrorConflict,
			Layout:          layout,
			Current:         cloneTranscriptDisplayDefault(current),
		},
	}
}

func transcriptDisplayPatchResponse(layout appwire.TranscriptViewportClass, current appwire.TranscriptDisplayDefault) appwire.TranscriptDisplayPatchResponse {
	return appwire.TranscriptDisplayPatchResponse{
		Layout:   layout,
		Revision: current.Revision,
		Config:   cloneTranscriptDisplayConfig(current.Config),
	}
}

func transcriptDisplaySnapshotLayout(state transcriptDisplaySnapshot, layout appwire.TranscriptViewportClass) appwire.TranscriptDisplayDefault {
	if layout == appwire.TranscriptViewportMobile {
		return cloneTranscriptDisplayDefault(state.Mobile)
	}
	return cloneTranscriptDisplayDefault(state.Desktop)
}

func transcriptDisplaySnapshotLayoutPtr(state *transcriptDisplaySnapshot, layout appwire.TranscriptViewportClass) *appwire.TranscriptDisplayDefault {
	if layout == appwire.TranscriptViewportMobile {
		return &state.Mobile
	}
	return &state.Desktop
}

func transcriptDisplayStatePath(stateRoot string) string {
	return filepath.Join(stateRoot, transcriptDisplayStateSubdir, transcriptDisplayStateFilename)
}

func loadTranscriptDisplaySnapshotFS(fs afero.Fs, stateRoot string) (transcriptDisplaySnapshot, error) {
	empty := transcriptDisplayShippedSnapshot()
	if stateRoot == "" {
		return empty, nil
	}
	data, err := afero.ReadFile(fs, transcriptDisplayStatePath(stateRoot))
	if os.IsNotExist(err) {
		return empty, nil
	}
	if err != nil {
		return empty, fmt.Errorf("read transcript display state: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return empty, nil
	}

	var state transcriptDisplaySnapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return empty, fmt.Errorf("decode transcript display state: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return empty, errors.New("decode transcript display state: trailing JSON value")
		}
		return empty, fmt.Errorf("decode transcript display state trailing data: %w", err)
	}
	if err := validateTranscriptDisplaySnapshotShape(data); err != nil {
		return empty, fmt.Errorf("validate transcript display state shape: %w", err)
	}
	if err := validateTranscriptDisplaySnapshot(state); err != nil {
		return empty, fmt.Errorf("validate transcript display state: %w", err)
	}
	return state, nil
}

func saveTranscriptDisplaySnapshotFS(fs afero.Fs, stateRoot string, state transcriptDisplaySnapshot, faults transcriptDisplayStoreFaults) (renamed bool, err error) {
	if err := validateTranscriptDisplaySnapshot(state); err != nil {
		return false, err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return false, fmt.Errorf("marshal transcript display state: %w", err)
	}
	if stateRoot == "" {
		return true, nil
	}

	path := transcriptDisplayStatePath(stateRoot)
	dir := filepath.Dir(path)
	if err := fs.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create transcript display state directory: %w", err)
	}
	temp, err := afero.TempFile(fs, dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp transcript display state: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if temp != nil {
			_ = temp.Close()
		}
		if !renamed {
			_ = fs.Remove(tempPath)
		}
	}()
	if _, err := temp.Write(data); err != nil {
		return false, fmt.Errorf("write temp transcript display state: %w", err)
	}
	if err := temp.Sync(); err != nil && !deletionSyncUnsupported(err) {
		return false, fmt.Errorf("sync temp transcript display state: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close temp transcript display state: %w", err)
	}
	temp = nil
	if faults.BeforeRename != nil {
		if err := faults.BeforeRename(); err != nil {
			return false, err
		}
	}
	if err := fs.Rename(tempPath, path); err != nil {
		return false, fmt.Errorf("rename transcript display state: %w", err)
	}
	renamed = true
	directory, err := fs.Open(dir)
	if err != nil {
		return true, fmt.Errorf("open transcript display state directory: %w", err)
	}
	if err := directory.Sync(); err != nil && !deletionSyncUnsupported(err) {
		_ = directory.Close()
		return true, fmt.Errorf("sync transcript display state directory: %w", err)
	}
	if err := directory.Close(); err != nil {
		return true, fmt.Errorf("close transcript display state directory: %w", err)
	}
	if faults.AfterRename != nil {
		if err := faults.AfterRename(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func validateTranscriptDisplaySnapshot(state transcriptDisplaySnapshot) error {
	if state.Version != transcriptDisplaySnapshotVersion {
		return fmt.Errorf("unsupported transcript display state version %d", state.Version)
	}
	if err := appwire.ValidateTranscriptDisplayConfig(state.Desktop.Config); err != nil {
		return fmt.Errorf("desktop config: %w", err)
	}
	if err := appwire.ValidateTranscriptDisplayConfig(state.Mobile.Config); err != nil {
		return fmt.Errorf("mobile config: %w", err)
	}
	return nil
}

func validateTranscriptDisplaySnapshotShape(raw []byte) error {
	top, err := transcriptDisplaySnapshotObjectFields(raw)
	if err != nil {
		return err
	}
	if err := requireTranscriptDisplaySnapshotFields(top, "version", "desktop", "mobile"); err != nil {
		return err
	}
	for _, layout := range []string{"desktop", "mobile"} {
		if err := validateTranscriptDisplaySnapshotDefaultShape(layout, top[layout]); err != nil {
			return err
		}
	}
	return nil
}

func validateTranscriptDisplaySnapshotDefaultShape(layout string, raw json.RawMessage) error {
	fields, err := transcriptDisplaySnapshotObjectFields(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", layout, err)
	}
	if err := requireTranscriptDisplaySnapshotFields(fields, "revision", "config"); err != nil {
		return fmt.Errorf("%s: %w", layout, err)
	}
	if bytes.Equal(bytes.TrimSpace(fields["revision"]), []byte("null")) {
		return fmt.Errorf("%s revision must be an unsigned integer", layout)
	}
	configFields, err := transcriptDisplaySnapshotObjectFields(fields["config"])
	if err != nil {
		return fmt.Errorf("%s config: %w", layout, err)
	}
	if err := requireTranscriptDisplaySnapshotFields(configFields, "version", "content", "advanced"); err != nil {
		return fmt.Errorf("%s config: %w", layout, err)
	}
	advancedFields, err := transcriptDisplaySnapshotObjectFields(configFields["advanced"])
	if err != nil {
		return fmt.Errorf("%s advanced: %w", layout, err)
	}
	if err := requireTranscriptDisplaySnapshotFields(advancedFields, "roundTimings", "tokenCounts", "estimatedCost", "systemEvents", "promptEvents", "hookExits"); err != nil {
		return fmt.Errorf("%s advanced: %w", layout, err)
	}
	if err := requireTranscriptDisplaySnapshotBooleans(advancedFields, "roundTimings", "tokenCounts", "estimatedCost", "systemEvents", "promptEvents"); err != nil {
		return fmt.Errorf("%s advanced: %w", layout, err)
	}

	contentFields, err := transcriptDisplaySnapshotObjectFields(configFields["content"])
	if err != nil {
		return fmt.Errorf("%s content: %w", layout, err)
	}
	if err := requireTranscriptDisplaySnapshotFields(contentFields, "kind"); err != nil {
		return fmt.Errorf("%s content: %w", layout, err)
	}
	var kind appwire.TranscriptContentKind
	if err := json.Unmarshal(contentFields["kind"], &kind); err != nil {
		return fmt.Errorf("%s content kind: %w", layout, err)
	}
	switch kind {
	case appwire.TranscriptContentKindPreset:
		if err := requireTranscriptDisplaySnapshotFields(contentFields, "level"); err != nil {
			return fmt.Errorf("%s content: %w", layout, err)
		}
		if _, ok := contentFields["custom"]; ok {
			return fmt.Errorf("%s content cannot contain both preset and custom representations", layout)
		}
	case appwire.TranscriptContentKindCustom:
		if err := requireTranscriptDisplaySnapshotFields(contentFields, "custom"); err != nil {
			return fmt.Errorf("%s content: %w", layout, err)
		}
		if _, ok := contentFields["level"]; ok {
			return fmt.Errorf("%s content cannot contain both custom and preset representations", layout)
		}
		customFields, err := transcriptDisplaySnapshotObjectFields(contentFields["custom"])
		if err != nil {
			return fmt.Errorf("%s custom: %w", layout, err)
		}
		if err := requireTranscriptDisplaySnapshotFields(customFields, "toolIntent", "toolCalls", "reasoning", "expandByDefault"); err != nil {
			return fmt.Errorf("%s custom: %w", layout, err)
		}
		if err := requireTranscriptDisplaySnapshotBooleans(customFields, "toolIntent", "toolCalls", "reasoning", "expandByDefault"); err != nil {
			return fmt.Errorf("%s custom: %w", layout, err)
		}
	default:
		// Semantic validation reports the canonical invalid-kind error.
	}
	return nil
}

func transcriptDisplaySnapshotObjectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, errors.New("expected JSON object")
	}
	return fields, nil
}

func requireTranscriptDisplaySnapshotFields(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}

func requireTranscriptDisplaySnapshotBooleans(fields map[string]json.RawMessage, names ...string) error {
	for _, name := range names {
		value := bytes.TrimSpace(fields[name])
		if !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
			return fmt.Errorf("field %q must be a boolean", name)
		}
	}
	return nil
}

func transcriptDisplayShippedSnapshot() transcriptDisplaySnapshot {
	defaults := appwire.TranscriptDisplayShippedDefaults()
	return transcriptDisplaySnapshot{
		Version: transcriptDisplaySnapshotVersion,
		Desktop: cloneTranscriptDisplayDefault(defaults.Desktop),
		Mobile:  cloneTranscriptDisplayDefault(defaults.Mobile),
	}
}

func transcriptDisplaySnapshotDefaults(state transcriptDisplaySnapshot) appwire.TranscriptDisplayDefaults {
	return appwire.TranscriptDisplayDefaults{
		Desktop: cloneTranscriptDisplayDefault(state.Desktop),
		Mobile:  cloneTranscriptDisplayDefault(state.Mobile),
	}
}

func cloneTranscriptDisplaySnapshot(state transcriptDisplaySnapshot) transcriptDisplaySnapshot {
	return transcriptDisplaySnapshot{
		Version: state.Version,
		Desktop: cloneTranscriptDisplayDefault(state.Desktop),
		Mobile:  cloneTranscriptDisplayDefault(state.Mobile),
	}
}

func cloneTranscriptDisplayDefault(value appwire.TranscriptDisplayDefault) appwire.TranscriptDisplayDefault {
	value.Config = cloneTranscriptDisplayConfig(value.Config)
	return value
}

func cloneTranscriptDisplayConfig(value appwire.TranscriptDisplayConfig) appwire.TranscriptDisplayConfig {
	if value.Content.Custom != nil {
		custom := *value.Content.Custom
		value.Content.Custom = &custom
	}
	return value
}
