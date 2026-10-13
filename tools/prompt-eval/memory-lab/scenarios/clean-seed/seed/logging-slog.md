# Logging goes through slog

New logging goes through log/slog. Never add new oldlog calls; existing ones stay until their own migration.

**Why:** Jesse decided on 2026-09-30 that oldlog is deleted in the 2.0 cleanup.

**How to apply:** when adding a log line anywhere in shop, use slog even if the surrounding file still uses oldlog.
