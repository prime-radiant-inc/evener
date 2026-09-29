//go:build !race

package hub

import "time"

const raceDetectorEnabled = false

// relayResyncRaceFloor is zero without the race detector: awaitRelayResync
// keeps the requested bound.
const relayResyncRaceFloor = 0 * time.Second
