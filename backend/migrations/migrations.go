// Package migrations embeds the SQL migrations so that the binary can apply them
// itself (`pigeondrop migrate up`) and sqlc can read the same files as its schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
