//go:build race

package hub

import "time"

const raceDetectorEnabled = true

// relayResyncRaceFloor is the minimum awaitRelayResync waits for a resync under
// the race detector. The hub's ordered relay goroutines run ~10x slower under
// -race and a loaded CI runner stretches that further, so a request-sized bound
// (5s) can expire while the resync is genuinely on its way (issue #2977).
const relayResyncRaceFloor = 30 * time.Second
