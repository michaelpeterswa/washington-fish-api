// Package migrations embeds the goose SQL migration files so they can be run
// from the wfa-worker `migrate` job without shipping the .sql files separately.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
