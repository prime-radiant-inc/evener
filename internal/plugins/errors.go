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
	// ErrPluginUninstalledCacheRemains marks Remove's applied-with-litter
	// outcome, the plugin analogue of
	// ErrMarketplaceUnregisteredCloneRemains: the removal save has already
	// landed (the plugin is gone from List) but its cache directory could
	// not be removed from disk. A caller distinguishes this from a plain
	// refusal by errors.Is against this instead of assuming a non-nil error
	// means the plugin is still installed.
	//
	// No production caller branches on it yet: the hub's plugin Remove
	// returns Remove's error verbatim, and its clients reconcile from the
	// evener/plugin/updated broadcast that saveRegistry's StoreChanged mark
	// already sends (issue #1634), so a client is not left showing a removed
	// plugin. RemoveMarketplace's wire parity (its ErrorMarketplace*
	// discriminator carrying the refreshed list) predates that broadcast and
	// is deliberately not mirrored here; wire-level parity for this sentinel
	// is deferred rather than dropped.
	ErrPluginUninstalledCacheRemains = errors.New("plugin uninstalled, but its cache could not be removed")
	// ErrMarketplaceSourceUnsupported rejects a source kind this build does not
	// accept; it is wire input, not a store failure, and carries no path.
	ErrMarketplaceSourceUnsupported = errors.New("unsupported marketplace source")
)
