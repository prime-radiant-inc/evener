package agent

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

var nativeMemoryToolNames = []string{"memory_read", "memory_write", "memory_edit", "memory_search", "memory_delete"}

// memorySaveToolNames are the native memory tools that change pages; a
// delegate gets only memoryReadToolNames.
var (
	memorySaveToolNames = []string{"memory_write", "memory_edit", "memory_delete"}
	memoryReadToolNames = []string{"memory_read", "memory_search"}
)

// memoryScopes lists every memory scope in projection order.
var memoryScopes = []string{"personal", "project"}

// memoryScopeBinding resolves scope to its directory under the memory state
// root. An error means the scope is unknown or not bound for this session.
func (s *Session) memoryScopeBinding(scope string) (string, error) {
	switch scope {
	case "personal":
		return "memory/personal", nil
	case "project":
		id := s.cfg.MemoryProjectID
		if id == "" || !filepath.IsLocal(id) || strings.ContainsAny(id, `/\\`) || id == "." {
			return "", errors.New("project memory is not bound")
		}
		return filepath.Join("memory", "projects", id), nil
	default:
		return "", fmt.Errorf("unknown memory scope %q", scope)
	}
}

// homeMemoryProjectID binds a session saved before project memory the way
// launch binds a new one, resolving its own home directory rather than
// wherever the resume was invoked. A home that is relative (it meant something
// only to the process that saved it) or no longer resolves leaves the session
// unbound.
func homeMemoryProjectID(env execenv.ExecutionEnvironment, meta schema.SessionMeta) string {
	home := meta.HomeDir()
	if !filepath.IsAbs(home) {
		return ""
	}
	// A local environment confines command working directories to its own
	// root, which is the resume directory, so the resolver's git fallback runs
	// from a clone rooted at home. The clone's probe scratch goes with it. A
	// sandbox that cannot re-root to home leaves the session unbound rather
	// than probing home unconfined.
	if local, ok := env.(*execenv.LocalExecutionEnvironment); ok {
		rooted := local.WithWorkingDirectory(home)
		defer func() { _ = rooted.DisposeSessionScratch() }()
		if rooted.SandboxReRootError() != nil {
			return ""
		}
		env = rooted
	}
	project, err := identifier.ResolveProjectWith(home, execenv.NewProjectResolver(env))
	if err != nil {
		return ""
	}
	return project.ID
}

// memoryReportReminder rides on the result tool's description because the
// model reads it at the moment it decides the work is done, which system
// prompt guidance alone did not reliably reach.
const memoryReportReminder = "Before a final answer with end_turn=true, save to memory what you learned in this session, whether you worked it out or were told it, that a future session would otherwise have to learn or figure out again. Saying it in your report does not save it."

func (s *Session) memoryContextEnabled() bool {
	return !s.cfg.DisableMemory && s.cfg.MemoryStateRoot != "" && s.reg != nil && s.reg.Get("memory_read") != nil
}

// memorySaveInstructionsEnabled gates text that tells the model to write or
// correct memory: a session that can read but not write memory still gets its
// indexes and read guidance, never instructions it cannot follow.
func (s *Session) memorySaveInstructionsEnabled() bool {
	if !s.memoryContextEnabled() {
		return false
	}
	for _, name := range memorySaveToolNames {
		if !s.canInstructTool(name) {
			return false
		}
	}
	return true
}

// memoryIndexFile is the scope's generated index, rendered from page
// frontmatter; once a writing session migrates a scope, no file by this name
// remains. memory_read renders the index for this name, the write tools
// refuse it, and memory_search never searches a file by this name.
const memoryIndexFile = "MEMORY.md"

// memoryIndexBaseline is the index the session already knows for a scope:
// the whole rendered index of a current scope, with the session's own page
// writes patched in, or that the scope has no pages. Its page lines are
// compared as a set, so their order carries no meaning.
type memoryIndexBaseline struct {
	status, index string
}

type memoryProjection struct {
	Scope, Status string
	// Content is the index as the model is shown it, within the projection
	// budget; Index is the whole rendering, which baselines and change
	// blocks compare, so a page moving past the budget is never a change.
	Content, Index string
	Truncated      bool
}

// memoryReadPageLimit bounds the pages per scope the session tracks for change
// notices, so each turn-start read stays within the refresh budget.
const memoryReadPageLimit = 32

// memoryPageRecord is what the session last knew of a memory page it read:
// the content's digest, or that the page was absent.
type memoryPageRecord struct {
	sum    [sha256.Size]byte
	absent bool
}

type memoryIndexFlight struct {
	done       chan struct{}
	projection memoryProjection
	// pages holds the current record of each read page the flight checked.
	pages map[string]memoryPageRecord
	// Guarded by memoryMu, the worker only sets projection and closes done.
	// stale marks a result that no longer reflects what the session may rely
	// on: the session's own write, compaction or revocation came after the
	// read began. A stale flight keeps its slot until done closes, then is
	// discarded for a fresh read. Any other flight is a genuine read: one a
	// boundary stopped waiting for keeps its slot, and once done the next
	// boundary publishes it instead of reading again, so a slow read cannot
	// keep every boundary from publishing.
	stale bool
}

type memoryEnvironmentFlight struct {
	done chan struct{}
	env  *execenv.LocalExecutionEnvironment
	err  error
}

// renderMemoryScope migrates a hand-written index if one remains and the
// session can write memory (memorySaveInstructionsEnabled), then renders the
// scope's pages into p: "current", or "missing" when the scope has no pages.
// Migration edits existing pages and removes the root MEMORY.md, so it needs
// the same write, edit and delete access the save instructions do; any other
// session renders the pages as they are, with fallback descriptions, until a
// writing session migrates. A failed migration never blocks the rendering;
// the next one retries it. A failed listing leaves p as it was.
func (s *Session) renderMemoryScope(env *execenv.LocalExecutionEnvironment, p *memoryProjection) error {
	if s.memorySaveInstructionsEnabled() {
		_ = migrateMemoryScope(env)
	}
	pages, err := listMemoryPages(env)
	if err != nil {
		return err
	}
	if len(pages) == 0 {
		p.Status, p.Content, p.Index, p.Truncated = "missing", "", "", false
		return nil
	}
	p.Status = "current"
	p.Content, p.Index, p.Truncated = projectMemoryIndex(pages, memoryProjectionCap)
	return nil
}

func (s *Session) unavailableMemoryToolNames() []string {
	var denied []string
	for _, name := range nativeMemoryToolNames {
		if s.reg == nil || s.reg.Get(name) == nil {
			denied = append(denied, name)
		}
	}
	return denied
}

// Profiles and extensions cannot advertise placeholders for disabled or
// unbound native memory. A delegate never saves memory, whatever its role
// grants: it reports what it learned to the session that started it, which
// decides what to keep, so a delegate keeps only read and search. Run after
// registration, before caching definitions.
func (s *Session) filterUnavailableMemoryTools() {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		for name := range s.reg.RegisteredNames() {
			if strings.HasPrefix(name, "memory_") {
				s.reg.Remove(name)
			}
		}
		return
	}
	if s.isSubagentSession() {
		for _, name := range memorySaveToolNames {
			s.reg.Remove(name)
		}
	}
}

// openMemoryEnvironment opens scope, creating its directory if it is absent.
func (s *Session) openMemoryEnvironment(scope string) (*execenv.LocalExecutionEnvironment, error) {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return nil, errors.New("memory is disabled or unbound")
	}
	relative, err := s.memoryScopeBinding(scope)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return nil, errors.New("session is closing")
	}
	s.memoryMu.Lock()
	if s.memoryClosed {
		s.memoryMu.Unlock()
		return nil, errors.New("session is closing")
	}
	if flight := s.memoryEnvFlights[scope]; flight != nil {
		s.memoryMu.Unlock()
		<-flight.done
		return flight.env, flight.err
	}
	flight := &memoryEnvironmentFlight{done: make(chan struct{})}
	if s.memoryEnvFlights == nil {
		s.memoryEnvFlights = make(map[string]*memoryEnvironmentFlight)
	}
	s.memoryEnvFlights[scope] = flight
	previous, root := s.memoryEnvs[scope], s.memoryRoots[scope]
	s.memoryMu.Unlock()

	// Root creation can stall too. Never hold session locks across host I/O.
	err = s.beforeMemoryIO(scope, "setup")
	var env *execenv.LocalExecutionEnvironment
	if err == nil && root == nil {
		root, err = execenv.NewConfinedFileRoot(s.cfg.MemoryStateRoot, relative)
	}
	if err == nil {
		env, err = root.Open(previous)
	}
	s.memoryMu.Lock()
	delete(s.memoryEnvFlights, scope)
	var retire *execenv.LocalExecutionEnvironment
	if s.memoryClosed {
		if env != previous {
			retire = env
		}
		env, err = nil, errors.New("session is closing")
	} else {
		if root != nil {
			if s.memoryRoots == nil {
				s.memoryRoots = make(map[string]*execenv.ConfinedFileRoot)
			}
			s.memoryRoots[scope] = root
		}
		// Keep previous alive on a qualification fault, but do not hand it out.
		// A later access retries beneath the same captured host authority.
		if env != nil {
			if s.memoryEnvs == nil {
				s.memoryEnvs = make(map[string]*execenv.LocalExecutionEnvironment)
			}
			s.memoryEnvs[scope] = env
			if previous != env && s.memoryEnvUsers[previous] == 0 {
				retire = previous
			}
		}
	}
	closed := s.memoryClosed
	s.memoryMu.Unlock()
	if retire != nil {
		retire.Cleanup()
	}
	if closed && root != nil {
		root.Close()
	}
	s.memoryMu.Lock()
	flight.env, flight.err = env, err
	close(flight.done)
	s.memoryMu.Unlock()
	return env, err
}

// Lease the environment before the external pre-I/O boundary. Requalification
// and Close stop admitting it, but cannot retire it until this operation ends.
func (s *Session) acquireMemoryEnvironment(scope string) (*execenv.LocalExecutionEnvironment, func(), error) {
	for {
		env, err := s.openMemoryEnvironment(scope)
		if err != nil {
			return nil, nil, err
		}
		s.memoryMu.Lock()
		if s.memoryClosed {
			s.memoryMu.Unlock()
			return nil, nil, errors.New("session is closing")
		}
		if s.memoryEnvs[scope] != env {
			s.memoryMu.Unlock()
			continue
		}
		if s.memoryEnvUsers == nil {
			s.memoryEnvUsers = make(map[*execenv.LocalExecutionEnvironment]int)
		}
		s.memoryEnvUsers[env]++
		s.memoryMu.Unlock()
		return env, func() {
			s.memoryMu.Lock()
			s.memoryEnvUsers[env]--
			unused := s.memoryEnvUsers[env] == 0
			if unused {
				delete(s.memoryEnvUsers, env)
			}
			retire := unused && (s.memoryClosed || s.memoryEnvs[scope] != env)
			s.memoryMu.Unlock()
			if retire {
				env.Cleanup()
			}
		}, nil
	}
}

func (s *Session) beforeMemoryIO(scope, operation string) error {
	if fn := s.cfg.testOnly.memoryBeforeIO; fn != nil {
		return fn(scope, operation)
	}
	return nil
}

func (s *Session) closeMemoryEnvironments() {
	s.memoryMu.Lock()
	s.memoryClosed = true
	var envs []*execenv.LocalExecutionEnvironment
	for _, env := range s.memoryEnvs {
		if s.memoryEnvUsers[env] == 0 {
			envs = append(envs, env)
		}
	}
	roots := s.memoryRoots
	s.memoryRoots = nil
	s.memoryEnvs = nil
	s.memoryMu.Unlock()
	for _, root := range roots {
		root.Close()
	}
	for _, env := range envs {
		// Shared confinement retires fds once an admitted file operation ends.
		// Index readers and late setup workers retire their own environments.
		env.Cleanup()
	}
}

func (s *Session) readMemoryIndex(scope string) memoryProjection {
	p, _ := s.readMemoryScope(scope, nil)
	return p
}

// readMemoryScope reads scope's index and, beneath the same environment
// lease, the current record of each named page.
func (s *Session) readMemoryScope(scope string, pages []string) (memoryProjection, map[string]memoryPageRecord) {
	p := memoryProjection{Scope: scope, Status: "unavailable"}
	env, release, err := s.acquireMemoryEnvironment(scope)
	if err != nil {
		return p, nil
	}
	defer release()
	// Admit before the pre-read boundary, not merely before ReadFileRaw. Close
	// must not retire this reusable environment before the reader acquires its
	// confined layer. One flight per scope makes this reader its sole owner.
	s.memoryMu.Lock()
	if s.memoryClosed {
		s.memoryMu.Unlock()
		return p, nil
	}
	if s.memoryIndexReaders == nil {
		s.memoryIndexReaders = make(map[string]*execenv.LocalExecutionEnvironment)
	}
	s.memoryIndexReaders[scope] = env
	s.memoryMu.Unlock()
	defer func() {
		s.memoryMu.Lock()
		delete(s.memoryIndexReaders, scope)
		s.memoryMu.Unlock()
	}()
	if err := s.beforeMemoryIO(scope, "index_read"); err != nil {
		return p, nil
	}
	_ = s.renderMemoryScope(env, &p) // a failed rendering leaves p unavailable
	records := make(map[string]memoryPageRecord, len(pages))
	for _, page := range pages {
		if record, ok := readMemoryPageRecord(env, page); ok {
			records[page] = record
		}
	}
	return p, records
}

// readMemoryPageRecord reads page's current record through env. It reports
// false when the page could not be read, so its last record stands.
func readMemoryPageRecord(env *execenv.LocalExecutionEnvironment, page string) (memoryPageRecord, bool) {
	return memoryPageRecordFrom(env.ReadFileRaw(filepath.Join(env.WorkingDirectory(), page)))
}

// memoryPageRecordFrom turns a page read into its record, reporting false
// when the read failed for any reason but absence.
func memoryPageRecordFrom(raw []byte, err error) (memoryPageRecord, bool) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return memoryPageRecord{absent: true}, true
	case err != nil:
		return memoryPageRecord{}, false
	}
	return memoryPageRecord{sum: sha256.Sum256(raw)}, true
}

// recordOwnMemoryWrite makes what the session wrote, edited or deleted what
// it knows: the written page's index line is patched into the scope's
// baseline, and a page it read gets its new record. Neither is echoed back.
// Both come from one read of the page at listed, the slash path the scope
// lists it at (listedMemoryPagePath), which also keys its read record.
//
// Only the written page is read back, so a page another session added,
// changed or deleted since the last boundary stays out of the baseline and
// still reaches the next boundary as a change. Another session writing this
// same page between this session's write and the read-back is folded in
// unseen until the page changes again or a compaction or resume delivers the
// index in full; the race is accepted as rare and cheap.
func (s *Session) recordOwnMemoryWrite(env *execenv.LocalExecutionEnvironment, scope, listed string) {
	file := filepath.FromSlash(listed)
	adopt := s.memoryOwnWriteSetsBaseline(scope) && isMemoryPagePath(listed)
	s.memoryMu.Lock()
	_, tracked := s.memoryReadPages[scope][file]
	s.memoryMu.Unlock()
	if !adopt && !tracked {
		s.recordOwnMemoryIndex(scope, false, listed, "", nil)
		return
	}
	// ioErr is the pre-I/O hook refusing the read, which forgets what the
	// session knew; a failed read itself is judged like any page read.
	var raw []byte
	var readErr error
	ioErr := s.beforeMemoryIO(scope, "record")
	var line string
	if ioErr == nil {
		raw, readErr = env.ReadFileRaw(filepath.Join(env.WorkingDirectory(), file))
		if page, exists := memoryPageFromRead(listed, raw, readErr, time.Time{}); exists {
			line = memoryIndexLine(page)
		}
	}
	s.recordOwnMemoryIndex(scope, adopt, listed, line, ioErr)
	if tracked {
		s.recordMemoryContent(scope, file, raw, cmp.Or(ioErr, readErr), false)
	}
}

// memoryOwnWriteSetsBaseline reports whether the session's own write to scope
// is patched into a baseline: the one it already has, or an empty one when
// the last projection found the scope missing, so the model knows it held
// nothing before the write. After any other state, such as unavailable, the
// model never saw the index; the scope stays unknown and the next boundary
// delivers it in full.
func (s *Session) memoryOwnWriteSetsBaseline(scope string) bool {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	_, known := s.memoryBaseline[scope]
	return known || s.memoryLastProjected[scope].Status == "missing"
}

// recordOwnMemoryIndex records the session's own write of the page at rel in
// scope. A read already in flight started before the write, so its result is
// discarded. When adopt holds, line (the page's index line, or "" when it no
// longer exists) replaces the page's old line in the baseline; a scope left
// with no page lines is missing. A failed read-back forgets the scope, so the
// next boundary delivers it in full.
func (s *Session) recordOwnMemoryIndex(scope string, adopt bool, rel, line string, err error) {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if flight := s.memoryIndexFlights[scope]; flight != nil {
		flight.stale = true
	}
	if !adopt {
		return
	}
	if err != nil {
		delete(s.memoryBaseline, scope)
		return
	}
	index := patchMemoryIndex(s.memoryBaseline[scope].index, rel, line)
	if index == "" {
		s.setMemoryBaselineLocked(scope, memoryIndexBaseline{status: "missing"})
		return
	}
	s.setMemoryBaselineLocked(scope, memoryIndexBaseline{status: "current", index: index})
}

// recordMemoryContent records raw, the result of reading page file (err when
// the read failed), as what the session knows of it. startTracking marks a
// page the session read with memory_read, which starts tracking it; raw is
// then exactly what that read loaded, so a change landing after it is
// noticed. A failed read untracks the page. A read already in flight started
// before this, so its result is discarded.
func (s *Session) recordMemoryContent(scope, file string, raw []byte, err error, startTracking bool) {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	if flight := s.memoryIndexFlights[scope]; flight != nil {
		flight.stale = true
	}
	record, ok := memoryPageRecordFrom(raw, err)
	if !ok {
		s.untrackMemoryPageLocked(scope, file)
		return
	}
	if s.memoryReadPages == nil {
		s.memoryReadPages = make(map[string]map[string]memoryPageRecord)
	}
	if s.memoryReadPages[scope] == nil {
		s.memoryReadPages[scope] = make(map[string]memoryPageRecord)
	}
	s.memoryReadPages[scope][file] = record
	if startTracking {
		s.noteMemoryPageReadLocked(scope, file)
	}
}

// noteMemoryPageReadLocked makes page the most recently read page of scope
// and, past memoryReadPageLimit, stops tracking the least recently read one.
// Only memory_read counts as reading; the session's own change to a tracked
// page updates its record without moving it. Callers hold memoryMu.
func (s *Session) noteMemoryPageReadLocked(scope, page string) {
	if s.memoryReadPageOrder == nil {
		s.memoryReadPageOrder = make(map[string][]string)
	}
	order := slices.DeleteFunc(s.memoryReadPageOrder[scope], func(p string) bool { return p == page })
	order = append(order, page)
	for len(order) > memoryReadPageLimit {
		delete(s.memoryReadPages[scope], order[0])
		order = order[1:]
	}
	s.memoryReadPageOrder[scope] = order
}

// untrackMemoryPageLocked stops tracking page. Callers hold memoryMu.
func (s *Session) untrackMemoryPageLocked(scope, page string) {
	delete(s.memoryReadPages[scope], page)
	if order, ok := s.memoryReadPageOrder[scope]; ok {
		s.memoryReadPageOrder[scope] = slices.DeleteFunc(order, func(p string) bool { return p == page })
	}
}

// memoryReadPagesFor lists the pages of scope the session has read.
func (s *Session) memoryReadPagesFor(scope string) []string {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	return slices.Sorted(maps.Keys(s.memoryReadPages[scope]))
}

// memoryPageChangesSection is the section naming each read page of scope
// whose content changed or that was removed since the session's record; it
// advances those records. It never carries page contents.
func (s *Session) memoryPageChangesSection(scope string, observed map[string]memoryPageRecord) memoryContextSection {
	return func() string {
		var changes []apptranscript.MemoryPageChange
		for _, page := range slices.Sorted(maps.Keys(observed)) {
			known, tracked := s.memoryReadPages[scope][page]
			now := observed[page]
			if !tracked || known == now {
				continue
			}
			s.memoryReadPages[scope][page] = now
			changes = append(changes, apptranscript.MemoryPageChange{Path: page, Removed: now.absent})
		}
		if len(changes) == 0 {
			return ""
		}
		return apptranscript.MemoryPageChangesSection(scope, changes)
	}
}

// Called only on the owner loop. A late result is not a read at this boundary.
// A new flight also checks the named read pages.
func (s *Session) memoryFlight(scope string, pages []string) *memoryIndexFlight {
	s.memoryMu.Lock()
	if s.memoryClosed {
		s.memoryMu.Unlock()
		return nil
	}
	if flight := s.memoryIndexFlights[scope]; flight != nil {
		select {
		case <-flight.done:
			if !flight.stale {
				// A late read that has since completed is published now.
				s.memoryMu.Unlock()
				return flight
			}
			delete(s.memoryIndexFlights, scope)
		default:
			s.memoryMu.Unlock()
			return flight
		}
	}
	flight := &memoryIndexFlight{done: make(chan struct{})}
	if s.memoryIndexFlights == nil {
		s.memoryIndexFlights = make(map[string]*memoryIndexFlight)
	}
	s.memoryIndexFlights[scope] = flight
	s.memoryMu.Unlock()
	go func() {
		flight.projection, flight.pages = s.readMemoryScope(scope, pages)
		close(flight.done)
	}()
	return flight
}

// memoryContextSection builds one section of a boundary's memory-context
// message. It runs under memoryMu, so the session state it updates and the
// decision to report agree, and returns "" when it has nothing to say. A nil
// section is skipped.
type memoryContextSection func() string

// appendMemoryContext appends one memory-context message holding every
// non-empty section, unless the session is closing; a closing session runs no
// section, so no state advances for news the model never receives. Every
// memory-context append goes through here, so a boundary never delivers more
// than one.
func (s *Session) appendMemoryContext(sections []memoryContextSection) {
	s.mu.Lock()
	closing := s.closing
	s.mu.Unlock()
	if closing {
		return
	}
	s.memoryMu.Lock()
	if s.memoryClosed {
		s.memoryMu.Unlock()
		return
	}
	var texts []string
	for _, section := range sections {
		if section == nil {
			continue
		}
		if text := section(); text != "" {
			texts = append(texts, text)
		}
	}
	s.memoryMu.Unlock()
	if len(texts) == 0 {
		return
	}
	msg := llm.UserMachinery(systemNotification(apptranscript.MemoryContextBody(texts)))
	s.appendTurnWithTranscriptMessage(schema.TurnMemoryContext, msg, msg)
}

// memoryIndexSection wraps body, the section reporting p, with the baseline
// it leaves. body reports whether the model now knows p's current index: its
// whole rendering, p.Index, becomes the baseline. Anything else forgets the
// scope, so the next current read delivers the full index: the model was last
// told the scope has no pages, that it could not be read, or that it is
// revoked, or was told nothing about a first such state.
func (s *Session) memoryIndexSection(p memoryProjection, body func() (text string, known bool)) memoryContextSection {
	return func() string {
		text, known := body()
		if known {
			s.setMemoryBaselineLocked(p.Scope, memoryIndexBaseline{status: p.Status, index: p.Index})
		} else {
			delete(s.memoryBaseline, p.Scope)
		}
		return text
	}
}

// memoryProjectionSection reports p in full, unless the model already has
// exactly p in context or p is a first observation of a scope with nothing in
// it.
func (s *Session) memoryProjectionSection(p memoryProjection) memoryContextSection {
	return s.memoryIndexSection(p, func() (string, bool) {
		if s.memoryLastProjected == nil {
			s.memoryLastProjected = make(map[string]memoryProjection)
		}
		if s.memoryEverProjected == nil {
			s.memoryEverProjected = make(map[string]bool)
		}
		prior, exists := s.memoryLastProjected[p.Scope]
		inContext := exists && prior == p
		suppressed := !inContext && !s.memoryEverProjected[p.Scope] && (p.Status == "missing" || p.Status == "revoked" || (p.Status == "current" && p.Content == ""))
		known := p.Status == "current" && !suppressed && p.Content != ""
		// A suppressed projection is recorded too: an own write reads the
		// state the scope was last found in, even one the model was not shown.
		s.memoryLastProjected[p.Scope] = p
		if inContext || suppressed {
			return "", known
		}
		s.memoryEverProjected[p.Scope] = true
		return apptranscript.MemoryIndexSection(p.Scope, p.Status, p.Truncated, p.Content), known
	})
}

// setMemoryBaselineLocked records baseline as the index the session knows for
// scope. Callers hold memoryMu.
func (s *Session) setMemoryBaselineLocked(scope string, baseline memoryIndexBaseline) {
	if s.memoryBaseline == nil {
		s.memoryBaseline = make(map[string]memoryIndexBaseline)
	}
	s.memoryBaseline[scope] = baseline
}

// memoryBaselineFor returns the index the session already knows for scope.
func (s *Session) memoryBaselineFor(scope string) (memoryIndexBaseline, bool) {
	s.memoryMu.Lock()
	defer s.memoryMu.Unlock()
	baseline, known := s.memoryBaseline[scope]
	return baseline, known
}

// memoryIndexDeltaCap bounds the bytes of one index-change section; a larger
// change is summarized as line counts with a route to memory_read.
const memoryIndexDeltaCap = 2048

// knownMemoryIndexSection reports a completed read of a scope whose index
// the session already knows. A current index delivers only the lines added
// and removed since the baseline, which then advances; the same index
// delivers nothing. That covers another session's edit and an index it
// created where the session had deleted its own. A missing index the session
// knows it deleted delivers nothing. Any other missing index, or a failed
// read, is projected as that state, as at any boundary. A read that missed
// the boundary budget or went stale never reaches here.
func (s *Session) knownMemoryIndexSection(baseline memoryIndexBaseline, p memoryProjection) memoryContextSection {
	switch p.Status {
	case "current":
		return s.memoryIndexSection(p, func() (string, bool) { return memoryIndexDeltaSection(baseline.index, p), true })
	case baseline.status:
		return nil
	default:
		return s.memoryProjectionSection(p)
	}
}

// memoryIndexDeltaSection lists the lines another session added to or
// removed from p's index since the baseline index, or is "" when no line
// changed. A change whose section would exceed memoryIndexDeltaCap is
// reported as line counts instead.
func memoryIndexDeltaSection(baseline string, p memoryProjection) string {
	added, removed := memoryIndexLineChanges(baseline, p.Index)
	if len(added) == 0 && len(removed) == 0 {
		return ""
	}
	if section := apptranscript.MemoryIndexChangesSection(p.Scope, added, removed); len(section) <= memoryIndexDeltaCap {
		return section
	}
	return apptranscript.MemoryIndexChangeCountsSection(p.Scope, len(added), len(removed))
}

// memoryIndexLineChanges compares two indexes line by line as multisets,
// comparing page lines only: added holds the lines of next not matched in prior,
// removed the lines of prior not matched in next, each in its file order.
func memoryIndexLineChanges(prior, next string) (added, removed []string) {
	unmatched := func(lines, against []string) []string {
		counts := make(map[string]int)
		for _, line := range against {
			counts[line]++
		}
		var out []string
		for _, line := range lines {
			if counts[line] > 0 {
				counts[line]--
				continue
			}
			out = append(out, line)
		}
		return out
	}
	priorLines, nextLines := memoryIndexLines(prior), memoryIndexLines(next)
	return unmatched(nextLines, priorLines), unmatched(priorLines, nextLines)
}

// memoryIndexLines returns an index's page lines; the tag header and the
// "Not shown" line are not page lines and never appear in a change section.
func memoryIndexLines(index string) []string {
	var lines []string
	for line := range strings.SplitSeq(index, "\n") {
		if strings.HasPrefix(line, "- ") {
			lines = append(lines, line)
		}
	}
	return lines
}

func (s *Session) resetMemoryProjectionAfterCompaction() {
	s.memoryMu.Lock()
	s.memoryLastProjected = nil
	s.memoryBaseline = nil
	for _, flight := range s.memoryIndexFlights {
		flight.stale = true
	}
	s.memoryMu.Unlock()
}

// Restored and forked history only seeds which scopes were observed, never
// historical bytes as a current read. Missing or revoked storage must supersede
// old observations. Only an index section counts as an observation: change
// and page sections say nothing about the index's state. Memory-context turns
// recorded in an earlier format seed nothing: that format is deliberately not
// decoded (no backward compatibility), so after an upgrade a scope's first
// missing or revoked state can go unannounced until its index next projects.
func (s *Session) restoreMemoryProjection(history []schema.Turn) {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return
	}
	s.memoryEverProjected = make(map[string]bool)
	for _, turn := range history {
		if len(s.memoryEverProjected) == len(memoryScopes) {
			break
		}
		if turn.Kind != schema.TurnMemoryContext {
			continue
		}
		sections, _ := apptranscript.ParseMemoryContext(turn.Message.Text())
		for _, section := range sections {
			if section.Index != nil && slices.Contains(memoryScopes, section.Scope) {
				s.memoryEverProjected[section.Scope] = true
			}
		}
	}
}

// memoryBoundaryBudget is how long a boundary waits for index reads before it
// projects a scope whose read has not finished as unavailable.
// memoryTestBoundaryBudget replaces it under `go test`, long enough that only
// a genuine hang reaches it.
const (
	memoryBoundaryBudget     = 250 * time.Millisecond
	memoryTestBoundaryBudget = 30 * time.Second
)

// memoryBoundaryWait is the boundary's read budget. Under `go test` it
// defaults long: -race on a loaded runner can slow an index read past 250ms,
// which would flake every test that asserts a projected index. A test whose
// subject is the budget sets testOnly.memoryRealBudget to get the production
// value. testing.Testing() is false outside `go test` binaries.
func (s *Session) memoryBoundaryWait() time.Duration {
	if testing.Testing() && !s.cfg.testOnly.memoryRealBudget {
		return memoryTestBoundaryBudget
	}
	return memoryBoundaryBudget
}

// maybeAppendMemoryContext projects each scope's generated index, never page contents.
// The model sees quoted lower-trust data inside core-owned currentness framing,
// every scope's news in one message.
// A scope gets its full index only while the session has no baseline for it:
// at start, after resume or compaction, and once storage, access or the index
// itself returns. turnStart marks the first model call of a turn; a scope the
// session already knows is read only then, never on the turn's later rounds.
func (s *Session) maybeAppendMemoryContext(ctx context.Context, turnStart bool) {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" || ctx.Err() != nil {
		return
	}
	timer := s.sclock().NewTimer(s.memoryBoundaryWait())
	defer timer.Stop()
	flights := make(map[string]*memoryIndexFlight)
	var sections []memoryContextSection
	for _, scope := range memoryScopes {
		if _, err := s.memoryScopeBinding(scope); !s.memoryContextEnabled() || err != nil {
			s.memoryMu.Lock()
			if flight := s.memoryIndexFlights[scope]; flight != nil {
				flight.stale = true
			}
			s.memoryMu.Unlock()
			sections = append(sections, s.memoryProjectionSection(memoryProjection{Scope: scope, Status: "revoked"}))
			continue
		}
		if _, known := s.memoryBaselineFor(scope); known && !turnStart {
			continue
		}
		var pages []string
		if turnStart {
			pages = s.memoryReadPagesFor(scope)
		}
		flight := s.memoryFlight(scope, pages)
		if flight == nil {
			return
		}
		flights[scope] = flight
	}
	var closed <-chan struct{}
	if s.sessionCtx != nil {
		closed = s.sessionCtx.Done()
	}
	for _, scope := range memoryScopes {
		flight := flights[scope]
		if flight == nil {
			continue
		}
		select {
		case <-flight.done:
		case <-timer.C():
			goto publish
		case <-ctx.Done():
			goto publish
		case <-closed:
			return
		}
	}
publish:
	for _, scope := range memoryScopes {
		flight := flights[scope]
		if flight == nil {
			continue
		}
		p := memoryProjection{Scope: scope, Status: "unavailable"}
		observed := false
		var pages map[string]memoryPageRecord
		s.memoryMu.Lock()
		select {
		case <-flight.done:
			if !flight.stale {
				p, pages, observed = flight.projection, flight.pages, true
			}
			delete(s.memoryIndexFlights, scope)
		default:
			// Still running: the slot stays for the next boundary.
		}
		s.memoryMu.Unlock()
		baseline, known := s.memoryBaselineFor(scope)
		switch {
		case known && !observed:
			// A read that missed the budget, or that the session's own write
			// made stale, observed nothing: what the session knows stands.
			continue
		case known:
			sections = append(sections, s.knownMemoryIndexSection(baseline, p))
		default:
			// Without a baseline, a read that missed the budget is projected
			// as unavailable, never presented as freshly read.
			sections = append(sections, s.memoryProjectionSection(p))
		}
		sections = append(sections, s.memoryPageChangesSection(scope, pages))
	}
	s.appendMemoryContext(sections)
}
