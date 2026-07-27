package hubcore

import (
	"sync"

	"primeradiant.com/serf/appwire"
)

// RemoteThreadCache holds the most recent remote-source thread list so a tree
// render never blocks on a network hop. A background refresher (main.go) calls
// Store on a ~30s ticker and on poke; the tree read path calls Get.
type RemoteThreadCache struct {
	mu         sync.RWMutex
	threads    []appwire.Thread
	complete   bool
	generation uint64
}

func (c *RemoteThreadCache) Store(threads []appwire.Thread) {
	c.StoreSnapshot(threads, true)
}

// StoreSnapshot stores the rows and whether they came from a complete
// authoritative source read. Generation changes for every stored snapshot so
// readers can keep tree construction and favorite revalidation on one source
// generation.
func (c *RemoteThreadCache) StoreSnapshot(threads []appwire.Thread, complete bool) {
	c.mu.Lock()
	c.threads = append([]appwire.Thread(nil), threads...)
	c.complete = complete
	c.generation++
	c.mu.Unlock()
}

func (c *RemoteThreadCache) Get() []appwire.Thread {
	snapshot := c.Snapshot()
	return snapshot.Threads
}

// RemoteThreadSnapshot is one cache generation read. Threads is a defensive
// copy and Complete describes the source read that produced it.
type RemoteThreadSnapshot struct {
	Threads    []appwire.Thread
	Complete   bool
	Generation uint64
}

func (c *RemoteThreadCache) Snapshot() RemoteThreadSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return RemoteThreadSnapshot{
		Threads:    append([]appwire.Thread(nil), c.threads...),
		Complete:   c.complete,
		Generation: c.generation,
	}
}
