// Package migrations embeds the versioned SQL migration files at
// /migrations (repo root) so the compiled server binary can self-migrate on
// startup without needing the migrate CLI or the source .sql files present
// on disk in the deployment environment (e.g. a minimal Docker image).
//
// The canonical, human-edited source of truth for these files is the
// repository's top-level migrations/ directory, versioned with
// golang-migrate's NNNN_description.{up,down}.sql naming convention. This
// package exists purely to make them embeddable, since Go's //go:embed
// cannot reach outside its own module subtree in a way that keeps the files
// at a conventional top-level location AND embeddable from internal/db.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
