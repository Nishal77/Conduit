// Package migrations embeds Conduit's SQL migration files into the compiled
// binary (via go:embed), so `conduit migrate` never needs the source tree
// available at runtime — consistent with ADR-001's single static binary goal.
package migrations

import "embed"

// FS holds every *.sql migration file in this directory. Consumed by
// internal/store's migration runner through golang-migrate's iofs source.
//
//go:embed *.sql
var FS embed.FS
