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
)
