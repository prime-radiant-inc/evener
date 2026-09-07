package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

// agentsDocPath is the personal AGENTS.md under the user config root - the
// same file agent.LoadInstructionDocs reads at session init.
func agentsDocPath(configRoot string) string {
	return filepath.Join(configRoot, agent.UserDocFile)
}

// readAgentsDoc reports the file as it is on disk. A missing file is the
// empty document, not an error: the settings section shows an empty editor
// and the first save creates it.
func readAgentsDoc(path string) (appwire.AgentsDocResponse, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return appwire.AgentsDocResponse{Path: path}, nil
	}
	if err != nil {
		return appwire.AgentsDocResponse{}, fmt.Errorf("AGENTS.md: read: %w", err)
	}
	return appwire.AgentsDocResponse{Path: path, Exists: true, Content: string(b)}, nil
}

// writeAgentsDoc replaces the file atomically (temp + rename, mode 0644,
// parent created), the same way registry.WriteConfigFile lands
// providers.toml beside it. Content is written byte for byte: this is the
// user's own prose, and trimming or appending a newline would make the
// editor disagree with the file it just saved.
func writeAgentsDoc(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("AGENTS.md: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		// A write that dies partway (ENOSPC, EIO) has already created the
		// temp file, so clear it too: the real file is untouched either way,
		// and a half-written AGENTS.md.tmp has no business outliving the
		// failure in the user's config root.
		_ = os.Remove(tmp)
		return fmt.Errorf("AGENTS.md: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("AGENTS.md: rename: %w", err)
	}
	return nil
}

// registerAgentsDocHandlers serves evener/settings/agentsDoc/{get,set}. Writes
// serialize on one mutex so two clients saving at once cannot interleave on
// the shared temp path; there is no revision check by design (spec
// 2026-09-07 §1) - the last write wins, and every client hears about it.
func registerAgentsDocHandlers(server *appserver.Server, configRoot string) {
	path := agentsDocPath(configRoot)
	var mu sync.Mutex
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocGet,
		func(context.Context, appwire.EmptyParams) (appwire.AgentsDocResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			return readAgentsDoc(path)
		})
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSettingsAgentsDocSet,
		func(_ context.Context, params appwire.AgentsDocSetParams) (appwire.AgentsDocResponse, error) {
			mu.Lock()
			defer mu.Unlock()
			if err := writeAgentsDoc(path, params.Content); err != nil {
				return appwire.AgentsDocResponse{}, err
			}
			resp, err := readAgentsDoc(path)
			if err != nil {
				// The rename landed, so the save APPLIED. Surfacing the
				// re-read failure would tell the requester its write was
				// rejected and leave every other client on the old content,
				// so describe what was just put on disk instead - the write
				// is byte for byte, so this is the file (same reasoning as
				// the post-rename path in registerKeybindingsHandlers).
				resp = appwire.AgentsDocResponse{Path: path, Exists: true, Content: params.Content}
			}
			server.BroadcastAll(appwire.NotifyEvenerSettingsAgentsDocChanged, resp)
			return resp, nil
		})
}
