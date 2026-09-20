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
	// ErrMarketplaceUnregisteredCloneRemains marks RemoveMarketplace's
	// applied-with-litter outcome: the unregister save has already landed
	// (the marketplace is gone from ListMarketplaces) but its clone could
	// not be removed from disk. A caller distinguishes this from a plain
	// refusal by errors.Is against this instead of assuming a non-nil error
	// means the marketplace is still registered.
	ErrMarketplaceUnregisteredCloneRemains = errors.New("marketplace unregistered, but its clone could not be removed")
	// ErrMarketplaceSourceUnsupported rejects a source kind this build does not
	// accept; it is wire input, not a store failure, and carries no path.
	ErrMarketplaceSourceUnsupported = errors.New("unsupported marketplace source")
)
