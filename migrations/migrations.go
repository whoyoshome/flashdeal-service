// Package migrations embeds the versioned SQL schema so tests and tools can
// apply exactly the files that ship with the code.
package migrations

import "embed"

// FS holds every *.up.sql and *.down.sql migration in this directory.
//
//go:embed *.sql
var FS embed.FS
