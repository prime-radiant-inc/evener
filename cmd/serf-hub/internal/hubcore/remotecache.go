package hubcore

import (
	"sync"

	"primeradiant.com/serf/appwire"
)

// RemoteThreadCache holds the most recent remote-source thread list so a tree
// render never blocks on a network hop. A background refresher (main.go) calls
// Store on a ~30s ticker and on poke; the tree read path calls Get.
type RemoteThreadCache struct {
	mu      sync.RWMutex
	threads []appwire.Thread
}

func (c *RemoteThreadCache) Store(threads []appwire.Thread) {
	c.mu.Lock()
	c.threads = append([]appwire.Thread(nil), threads...)
	c.mu.Unlock()
}

func (c *RemoteThreadCache) Get() []appwire.Thread {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.threads == nil {
		return nil
	}
	return append([]appwire.Thread(nil), c.threads...)
}
