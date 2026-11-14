// Package migrations embeds the SQL migrations of this service.
package migrations

import "embed"

// FS holds the goose SQL migration files.
//
//go:embed *.sql
var FS embed.FS
