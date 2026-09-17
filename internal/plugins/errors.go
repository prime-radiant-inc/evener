package plugins

import "errors"

var (
	ErrNotInstalled        = errors.New("plugin not installed")
	ErrMarketplaceNotFound = errors.New("marketplace not found")
	ErrMarketplaceExists   = errors.New("marketplace already registered")
	ErrPluginNotFound      = errors.New("plugin not found in marketplace")
	// ErrInvalidName rejects a marketplace/plugin name that cannot be a
	// filesystem path segment; it is caller input, not a store failure.
	ErrInvalidName = errors.New("must be a single non-traversing path component")
	// ErrMarketplaceSourceInStore refuses a directory source that names a
	// directory the store manages, whose contents the store rewrites without
	// asking whatever a marketplace is sourced from.
	ErrMarketplaceSourceInStore = errors.New("the source must be a directory outside the plugin store")
	// ErrStoreChanged marks a failure that left the store changed rather than
	// back as it was found: the write this call made stands. A caller that
	// announces applied writes to other clients (the hub broadcasts
	// evener/plugin/updated and evener/marketplace/updated) must announce one
	// of these too, because the listing every other client holds is stale by
	// exactly as much as after a clean write. Every store-changed state wraps
	// it, so one errors.Is answers the question for all of them.
	ErrStoreChanged = errors.New("the plugin store was changed")
)
