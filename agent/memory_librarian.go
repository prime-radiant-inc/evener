package agent

import (
	"os"
	"slices"
	"strings"

	"primeradiant.com/evener/agent/internal/tool"
)

// memoryLibrarianRole is the runtime-only role that authors and gardens
// memory when the memory librarian is on. It is the one delegate that keeps
// the memory save tools, and it gets them by identity: the runtime starts it
// with delegateArgs.runtimeRole, which no model call can set.
const memoryLibrarianRole = "memory-librarian"

// memoryLibrarianMode is which arm of the memory librarian spike a root runs.
type memoryLibrarianMode string

const (
	librarianOff     memoryLibrarianMode = "off"
	librarianTurn    memoryLibrarianMode = "turn"
	librarianSidecar memoryLibrarianMode = "sidecar"
)

// memoryLibrarianEnv selects the arm when SessionConfig.MemoryLibrarian is
// empty, so `evener run` and `evener serve` pick it up without new flags.
const memoryLibrarianEnv = "EVENER_MEMORY_LIBRARIAN"

// librarianMode reports this session's librarian arm. Only a root session
// with memory on runs one; every delegate, and any unknown value, is off.
func (s *Session) librarianMode() memoryLibrarianMode {
	if s == nil || s.isSubagentSession() || s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return librarianOff
	}
	mode := s.cfg.MemoryLibrarian
	if mode == "" {
		mode = os.Getenv(memoryLibrarianEnv)
	}
	switch memoryLibrarianMode(strings.TrimSpace(strings.ToLower(mode))) {
	case librarianTurn:
		return librarianTurn
	case librarianSidecar:
		return librarianSidecar
	default:
		return librarianOff
	}
}

// selectRuntimeRoleModel selects a runtime-only role on this session's own
// model and profile.
func (s *Session) selectRuntimeRoleModel(role string) (subagentModelSelection, error) {
	agent, err := runtimeRoleAgent(role)
	if err != nil {
		return subagentModelSelection{}, err
	}
	return subagentModelSelection{agent: &agent, profile: s.currentProfile()}, nil
}

// librarianToolNameCeiling adds back to a delegate's tool ceiling the memory
// save tools that stableDelegateToolNameCeiling strips from every delegate,
// limited to the ones the role lists. The parent's memory_read stands for
// memory being on: a root under the librarian has had its own save tools
// removed (filterUnavailableMemoryTools), so they can't be looked up there.
func librarianToolNameCeiling(reg *tool.Registry, ceiling, roleTools []string) []string {
	if reg == nil || reg.Get("memory_read") == nil {
		return ceiling
	}
	out := slices.Clone(ceiling)
	for _, name := range memorySaveToolNames {
		if slices.Contains(roleTools, name) && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}
