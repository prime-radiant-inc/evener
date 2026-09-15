package appsource

// OnlineSource is implemented by sources whose availability can change.
// Readers must treat a source that does not implement it as online.
type OnlineSource interface{ Online() bool }
