package agent

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"strings"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/tool"
)

func registerMemoryTools(reg *tool.Registry, s *Session) error {
	if s.cfg.DisableMemory || s.cfg.MemoryStateRoot == "" {
		return nil
	}
	tools := []tool.RegisteredTool{
		{Definition: tool.DefMemoryRead(), ReadOnly: true, Exec: s.execMemoryRead},
		{Definition: tool.MemoryDefinition(tool.DefWriteFile(), "memory_write"), Exec: s.execMemoryWrite},
		{Definition: tool.MemoryDefinition(tool.DefEditFile(), "memory_edit"), Exec: s.execMemoryEdit},
		{Definition: tool.MemoryDefinition(tool.DefGrep(), "memory_search"), ReadOnly: true, Exec: s.execMemorySearch},
		{Definition: tool.DefMemoryDelete(), Exec: s.execMemoryDelete},
	}
	for _, registered := range tools {
		if err := reg.Register(registered); err != nil {
			return err
		}
	}
	return nil
}

// memoryFileArgs changes only path authority; shared executors own file semantics.
func (s *Session) memoryFileArgs(args map[string]any, key, operation string) (*execenv.LocalExecutionEnvironment, map[string]any, func(), error) {
	scope := stringArg(args, "scope")
	if _, err := s.memoryScopeBinding(scope); err != nil {
		return nil, nil, nil, err
	}
	path := stringArg(args, key)
	if key == "path" && path == "" {
		path = "."
	}
	if !filepath.IsLocal(path) {
		return nil, nil, nil, errors.New("memory path must be relative and remain in its scope")
	}
	env, release, err := s.acquireMemoryEnvironment(scope)
	if err != nil {
		return nil, nil, nil, err
	}
	forwarded := maps.Clone(args)
	forwarded[key] = filepath.Join(env.WorkingDirectory(), path)
	if err := s.beforeMemoryIO(scope, operation); err != nil {
		release()
		return nil, nil, nil, err
	}
	return env, forwarded, release, nil
}

// errMemoryIndexGenerated refuses a write, edit or delete of the index.
var errMemoryIndexGenerated = errors.New(tool.MemoryIndexGenerated)

// isMemoryIndexPath reports whether file, cleaned and relative to the scope,
// names the generated index. Case is ignored: on a case-insensitive
// filesystem memory.md is the same file.
func isMemoryIndexPath(file string) bool {
	return strings.EqualFold(file, memoryIndexFile)
}

const (
	memoryMissingDescriptionNote    = "\n\nThis page has no description in its frontmatter, so its index line falls back to its first heading. Add description: <one line> to the frontmatter."
	memoryUnreadableFrontmatterNote = "\n\nThis page's frontmatter is not valid YAML, so its index line falls back to its first heading. Fix the frontmatter; quote a description that contains a colon."
)

// stampMemoryPage is raw, the bytes a tool is about to write to the file at
// rel, the slash path its scope lists it at, with the updated and by stamps
// set when the file is a Markdown page, and the notes the tool result should
// end with. The stamps go into the tool's one write, so no second
// read-modify-write can undo another session's write or delete. Any other
// file passes through unchanged.
func (s *Session) stampMemoryPage(rel string, raw []byte) (stamped []byte, notes string) {
	if path.Ext(rel) != ".md" || !isMemoryPagePath(rel) {
		return raw, ""
	}
	// updated is written by hand, unquoted, so it reads back as a YAML date.
	// Valid frontmatter that can't take a key line, such as a flow mapping,
	// stays unstamped rather than unreadable.
	stamped = setMemoryFrontmatterField(raw, "updated: "+s.sclock().Now().UTC().Format(time.DateOnly)+"\n")
	stamped = setMemoryFrontmatterField(stamped, memoryYAMLField("by", s.ID()))
	switch page := parseMemoryPage(rel, stamped, time.Time{}); {
	case page.Unreadable:
		notes = memoryUnreadableFrontmatterNote
	case !page.HasDescription:
		notes = memoryMissingDescriptionNote
	}
	return stamped, notes
}

// execOwnMemoryWrite runs a write, edit or delete of one memory file. It
// refuses the generated index. write passes the bytes it is about to write
// through stamp. Once the operation succeeds the result gets the page's
// notes and is recorded as the session's own: the page's line is patched
// into its index baseline, and a page it read gets its new record, so
// neither is echoed back.
func (s *Session) execOwnMemoryWrite(args map[string]any, operation string, write func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any, stamp func([]byte) []byte) (any, error)) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	if isMemoryIndexPath(file) {
		return nil, errMemoryIndexGenerated
	}
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", operation)
	if err != nil {
		return nil, err
	}
	defer release()
	// The page is stamped and its line patched under the path its scope
	// lists it at, which on a case-insensitive filesystem can differ from
	// file's case. It is looked up before the operation, while a page being
	// deleted still exists, and again after a write, so a page it creates is
	// listed under its own name.
	listed := listedMemoryPagePath(env, filepath.ToSlash(file))
	var notes string
	stamp := func(raw []byte) []byte {
		var stamped []byte
		stamped, notes = s.stampMemoryPage(listed, raw)
		return stamped
	}
	out, err := write(env, forwarded, stamp)
	if err != nil {
		return out, err
	}
	if operation != "delete" {
		listed = listedMemoryPagePath(env, filepath.ToSlash(file))
	}
	if text, ok := out.(string); ok && notes != "" {
		out = text + notes
	}
	s.recordOwnMemoryWrite(env, scope, listed)
	return out, nil
}

func (s *Session) execMemoryWrite(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "write", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any, stamp func([]byte) []byte) (any, error) {
		forwarded["content"] = string(stamp([]byte(fmt.Sprint(forwarded["content"]))))
		return execFileWrite(ctx, env, forwarded, s.fileReadGuard(env))
	})
}

// memoryPageSizeLimit is the size, in bytes, past which memory_read notes that
// a page is long.
const memoryPageSizeLimit = 4096

// memoryPageSizeNote is the note memory_read appends to a page of size bytes.
// It points at the gardening-memory skill only when the session can use it;
// otherwise it says what a page should hold.
func memoryPageSizeNote(size int, withSkill bool) string {
	kb := (size + 1023) / 1024 // round up, so a page just over the limit never reads as at it
	if withSkill {
		return fmt.Sprintf("\n\nThis page is long (%d KB). Use the gardening-memory skill to learn how to fix it.", kb)
	}
	return fmt.Sprintf("\n\nThis page is long (%d KB). A memory page should hold one fact.", kb)
}

// memoryGardeningSkillAvailable reports whether the session can load the
// gardening-memory skill: use_skill is callable and the skill is advertised
// to the model.
func (s *Session) memoryGardeningSkillAvailable() bool {
	if !s.canInstructTool("use_skill") {
		return false
	}
	for _, descriptor := range s.skills.ModelEntries() {
		if descriptor.CatalogName == "gardening-memory" {
			return true
		}
	}
	return false
}

func (s *Session) execMemoryRead(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	scope, file := stringArg(args, "scope"), filepath.Clean(stringArg(args, "file_path"))
	env, forwarded, release, err := s.memoryFileArgs(args, "file_path", "read")
	if err != nil {
		return nil, err
	}
	defer release()
	if isMemoryIndexPath(file) {
		var rendered memoryProjection
		if err := s.renderMemoryScope(env, &rendered); err != nil {
			return nil, err
		}
		if rendered.Status == "missing" {
			return "This scope has no pages yet.", nil
		}
		return execenv.NumberLines(strings.TrimSuffix(rendered.Index, "\n"), optionalIntArg(args, "offset"), optionalIntArg(args, "limit")), nil
	}
	// Capture the bytes this read loaded, so the page's record is exactly what
	// the session saw. They are the whole page even for an offset or limit
	// read, which therefore records the whole page as of that read.
	var raw []byte
	out, err := execFileReadWith(forwarded, s.fileReadGuard(env), func(path string, offset, limit *int) (string, error) {
		text, loaded, err := env.ReadFileAndBytes(path, offset, limit)
		raw = loaded
		return text, err
	})
	// Every page read is tracked for change notices.
	if err == nil {
		size := len(raw)
		// Another session may change the page between the read and its
		// record; the record still holds what this read loaded.
		recordErr := s.beforeMemoryIO(scope, "record")
		if recordErr != nil {
			raw = nil
		}
		// Keyed as the scope lists the page, so a write naming it in another
		// case finds this record.
		listed := filepath.FromSlash(listedMemoryPagePath(env, filepath.ToSlash(file)))
		s.recordMemoryContent(scope, listed, raw, recordErr, true)
		// A long page gets a note pointing at what a page should be.
		if text, ok := out.(string); ok && size > memoryPageSizeLimit {
			out = text + memoryPageSizeNote(size, s.memoryGardeningSkillAvailable())
		}
	}
	return out, err
}
func (s *Session) execMemoryEdit(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "edit", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any, stamp func([]byte) []byte) (any, error) {
		return execFileEditWith(forwarded, s.fileReadGuard(env), func(path, oldString, newString string, replaceAll bool) (string, error) {
			return env.EditFileWith(path, oldString, newString, replaceAll, stamp)
		})
	})
}

// execMemorySearch searches a scope's files. A scope a session without
// memory_write has not migrated still has its hand-written root MEMORY.md,
// which memory_read answers with the generated index instead, so the search
// never searches that file.
func (s *Session) execMemorySearch(ctx context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	env, forwarded, release, err := s.memoryFileArgs(args, "path", "search")
	if err != nil {
		return nil, err
	}
	defer release()
	target := filepath.Clean(stringArg(args, "path"))
	if isMemoryIndexPath(target) {
		return "", nil
	}
	var skip func(rel string) bool
	if target == "." {
		skip = isMemoryIndexPath
	}
	g := parseFileGrepArgs(forwarded)
	return env.GrepSkipping(ctx, g.pattern, g.path, g.glob, g.caseInsensitive, g.maxResults, g.outputMode, g.contextLines, skip)
}
func (s *Session) execMemoryDelete(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
	return s.execOwnMemoryWrite(args, "delete", func(env *execenv.LocalExecutionEnvironment, forwarded map[string]any, _ func([]byte) []byte) (any, error) {
		path := stringArg(forwarded, "file_path")
		warn := s.fileReadGuard(env).ReadBeforeWriteWarning(path)
		if err := env.RemoveConfinedFile(path); err != nil {
			return nil, err
		}
		return warn + "Removed or already absent: " + path, nil
	})
}
