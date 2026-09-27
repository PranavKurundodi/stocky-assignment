// Package migrations embeds the SQL migration files into the binary, so the
// server can apply them at startup without the files on disk.
package migrations

import "embed"

// FS holds every *.sql file in this directory.
//
//go:embed *.sql
var FS embed.FS
