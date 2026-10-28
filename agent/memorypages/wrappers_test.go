package memorypages

import (
	"context"

	"primeradiant.com/evener/agent/execenv"
)

// moveLegacyMemoryIndex moves the migrated hand-written index at legacy,
// whose bytes the migration read as raw, to a backup in root, trying the
// name backup first (see backUpMemoryIndex), then removes MEMORY.md only if
// it still holds raw (see removeMigratedMemoryIndex). No lock guards the
// move: another migration, or an older build writing MEMORY.md again, may
// change the scope at any point, and neither step relies on it holding
// still. A failure leaves the index in place for the next run.
func moveLegacyMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte, backup string) error {
	return moveLegacyMemoryIndexCtx(context.Background(), env, root, legacy, raw, backup)
}

// backUpMemoryIndex keeps raw, an index's bytes, as a backup in root, trying
// the name backup first, unless a backup already holds raw byte for byte, so
// alternating builds that write the same index add nothing. The backup is a
// copy of its own, so nothing written to MEMORY.md later reaches it. A
// backup is never replaced or removed, since each holds what a person or an
// older build wrote: the copy is written in full under a private name, then
// hard-linked to the backup name, which fails rather than replace a file put
// there since the name was chosen, and a taken name moves on to the next free
// one. Any other link failure, such as a filesystem without hard links, is
// returned.
func backUpMemoryIndex(env *execenv.LocalExecutionEnvironment, root string, raw []byte, backup string) error {
	return backUpMemoryIndexCtx(context.Background(), env, root, raw, backup)
}

// removeMigratedMemoryIndex removes the index at legacy only if it holds
// raw. A read followed by an unlink could delete a rewrite landing between
// the two, and no filesystem call removes a name only if its contents match.
// So whatever legacy names is first renamed, atomically, to a name only this
// run uses: a rewrite either lands before the rename and is captured, or
// after it, as a new MEMORY.md that stays. The captured file is removed only
// if it holds raw. Otherwise it goes back to MEMORY.md, or, when MEMORY.md
// was written yet again meanwhile, to a backup of its own; either way
// errLegacyMemoryIndexChanged reports that an index is left for the next
// run. A captured file that can't be put back keeps its private name, where
// the next migration finds it.
func removeMigratedMemoryIndex(env *execenv.LocalExecutionEnvironment, root, legacy string, raw []byte) error {
	return removeMigratedMemoryIndexCtx(context.Background(), env, root, legacy, raw)
}

// migrateMemoryScope moves a scope's hand-written index (see
// legacyMemoryIndexes) into its pages: each linked page with no description
// gets the one its index line gave, then the index is moved to a backup
// (see moveLegacyMemoryIndex). It is idempotent and takes no lock (the write
// loop and moveLegacyMemoryIndex say why), and the run that finds the index
// already moved is done. A linked target that can't be read (missing, a directory, a
// refused symlink), or whose frontmatter does not parse or can't take the
// description in place, is skipped. A page that fails to write does not stop
// the others; any failed write, or failing to read or move the index,
// returns an error and leaves the index in place, so the next run finishes
// the job.
func migrateMemoryScope(env *execenv.LocalExecutionEnvironment) error {
	return MigrateScopeCtx(context.Background(), env)
}

// migrateLegacyMemoryIndexes is migrateMemoryScope's work for the root
// indexes legacyMemoryIndexes found, in that order.
func migrateLegacyMemoryIndexes(env *execenv.LocalExecutionEnvironment, legacies []string) error {
	return migrateLegacyMemoryIndexesCtx(context.Background(), env, legacies)
}
