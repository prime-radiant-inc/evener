package skill

// GardeningMemorySkillName is the bundled skill whose instructions correct
// memory pages with the memory save tools.
const GardeningMemorySkillName = "gardening-memory"

// SessionAvailability is the session capability a skill advertisement needs
// beyond the source's own controls. The owning Session computes it for a live
// catalog; a cold hub catalog reads the same answer from persisted session
// metadata.
type SessionAvailability struct {
	// MemorySaveAvailable reports that the session serves memory_write,
	// memory_edit and memory_delete.
	MemorySaveAvailable bool
}

// SkillAvailable reports whether a session with this availability may advertise
// and load name. Every skill but gardening-memory is available as its source
// controls say.
func (a SessionAvailability) SkillAvailable(name string) bool {
	if name == GardeningMemorySkillName {
		return a.MemorySaveAvailable
	}
	return true
}

// UserEntriesForSession is UserEntries narrowed by a session's availability,
// preserving canonical order.
func (c Catalog) UserEntriesForSession(availability SessionAvailability) []Descriptor {
	entries := c.UserEntries()
	result := make([]Descriptor, 0, len(entries))
	for _, entry := range entries {
		if availability.SkillAvailable(entry.CatalogName) {
			result = append(result, entry)
		}
	}
	return result
}
