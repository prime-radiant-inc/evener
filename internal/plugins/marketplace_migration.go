package plugins

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// An older evener could record a marketplace under a name the store refuses
// today: one carrying '@', which splitKey reads as the plugin/marketplace
// separator, so its installs key as some shorter marketplace's; one of the
// two scratch names, whose directory the next fetch clears; or one that is
// not a single non-traversing path component, from which no store directory
// can be derived. The store renames such an entry the first time it takes
// its lock (lockStore), so that under the lock every recorded name is valid
// and no operation has to ask which evener wrote the name.

// migrateMarketplaceNames renames every recorded marketplace whose name
// validNameComponent refuses, longest name first, and says on stderr what
// each became. Each entry is saved on its own, so a failure leaves the
// entries before it migrated and the failing one as it was found, and the
// error names it. The caller must hold the store lock.
func (m *Manager) migrateMarketplaceNames() error {
	mk, err := m.loadMarketplaces()
	if err != nil {
		return err
	}
	names := refusedMarketplaceNames(mk)
	if len(names) == 0 {
		return nil
	}
	reg, err := m.loadRegistry()
	if err != nil {
		return err
	}
	for _, name := range names {
		newName, err := m.freeMarketplaceName(migratedMarketplaceName(name), mk, reg)
		if err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts: %w", name, err)
		}
		if reg, err = m.migrateMarketplaceName(mk, reg, name, newName); err != nil {
			return fmt.Errorf("renaming marketplace %q, recorded under a name the store no longer accepts, to %q: %w", name, newName, err)
		}
		_, _ = fmt.Fprintf(m.stderr(), "warning: marketplace %q was recorded under a name the store no longer accepts; renamed it to %q\n", name, newName)
	}
	return nil
}

// refusedMarketplaceNames is every recorded name validNameComponent refuses,
// longest first and otherwise sorted. Length decides the order because a key
// ends in "@<name>" for every recorded name that is a suffix of it, and only
// a longer name can share a shorter one's suffix: once the longer names have
// moved, every key still ending in "@<name>" is that entry's own to re-key.
func refusedMarketplaceNames(mk Marketplaces) []string {
	var names []string
	for name := range mk {
		if validNameComponent("marketplace", name) != nil {
			names = append(names, name)
		}
	}
	slices.SortFunc(names, func(a, b string) int {
		if len(a) != len(b) {
			return len(b) - len(a)
		}
		return strings.Compare(a, b)
	})
	return names
}

// migratedMarketplaceName derives the name a refused one is renamed to: path
// separators split it and the "." and ".." components go, the rest joined
// by '-'; every '@' becomes '-'; a scratch name loses its dot; and a name
// with nothing left, or one the store still refuses, becomes "marketplace".
func migratedMarketplaceName(name string) string {
	var parts []string
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part != "." && part != ".." {
			parts = append(parts, part)
		}
	}
	derived := strings.ReplaceAll(strings.Join(parts, "-"), "@", "-")
	if derived == stagingCloneName || derived == asideCloneName {
		derived = derived[1:]
	}
	if validNameComponent("marketplace", derived) != nil {
		return "marketplace"
	}
	return derived
}

// freeMarketplaceName is base, or the first of base-2, base-3, … that nothing
// occupies: no recorded marketplace, and none of the residue a rename onto
// it would bury (refuseLeftoversUnder).
func (m *Manager) freeMarketplaceName(base string, mk Marketplaces, reg Registry) (string, error) {
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		if _, recorded := mk[candidate]; recorded {
			continue
		}
		err := m.refuseLeftoversUnder(candidate, reg)
		if errors.Is(err, ErrMarketplaceExists) {
			continue
		}
		if err != nil {
			return "", err
		}
		return candidate, nil
	}
}

// migrateMarketplaceName renames one recorded marketplace the way an edit
// does — directories, registry keys, both files, with the same rollback —
// and returns the registry as saved. A recorded name that is not a path
// component is the exception: <marketplaces>/<name> and cache/<name> joined
// from it are directories outside the store, not this marketplace's, so
// nothing moves and no install path changes; only the record and its keys
// do.
func (m *Manager) migrateMarketplaceName(mk Marketplaces, reg Registry, name, newName string) (Registry, error) {
	ref, registryAsFound := mk[name], reg
	var undo []func() error
	if unsafePathComponent(name) {
		reg = rekeyRegistry(reg, name, newName, "", "")
	} else {
		var err error
		if ref, reg, undo, err = m.moveMarketplace(name, newName, ref, reg); err != nil {
			return registryAsFound, err
		}
	}
	if err := m.saveRename(mk, name, newName, ref, reg, registryAsFound); err != nil {
		return registryAsFound, errors.Join(err, runUndo(undo))
	}
	return reg, nil
}
