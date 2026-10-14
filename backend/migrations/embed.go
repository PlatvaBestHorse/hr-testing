package migrations

import "embed"

// Files contains immutable, ordered SQL migrations.
//
//go:embed *.sql
var Files embed.FS
