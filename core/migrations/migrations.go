// Package migrations embeds the SQL migrations applied by `core migrate` (NFR-D4).
// Migrations are forward-only and backwards-compatible across one release.
package migrations

import "embed"

// FS holds the goose migration files.
//
//go:embed *.sql
var FS embed.FS
