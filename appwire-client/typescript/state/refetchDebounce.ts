// How long a store waits after a hub change notification before re-reading.
// One value for every store so a burst of notifications coalesces the same way
// everywhere.
export const REFETCH_DEBOUNCE_MS = 250;
