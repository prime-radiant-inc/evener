// Package plugins is serf's manager for Claude Code-compatible plugin
// marketplaces and installed plugins. It owns the on-disk state under
// ~/.config/serf/plugins/ (known_marketplaces.json, installed_plugins.json,
// cloned marketplaces, and the materialized plugin cache) and exposes a
// Manager with marketplace and plugin lifecycle operations. It shells out to
// git for fetching and reuses agent/plugin.Load to validate materialized
// plugins. See docs/superpowers/specs/2026-07-04-plugin-marketplaces-design.md.
package plugins
