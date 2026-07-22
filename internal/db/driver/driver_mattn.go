//go:build mattnsqlite
// +build mattnsqlite

// Package driver selects the underlying SQLite driver via build
// tags. modernc.org/sqlite (pure Go) is the default; the mattn
// driver (CGO) takes over when the build tag `mattnsqlite` is set.
// Call sites use DriverName to open connections so they don't
// have to know which driver is active.
package driver

import (
	"net/url"
	"strconv"

	_ "github.com/mattn/go-sqlite3"
)

// DriverName is the database/sql driver name used by the
// currently-selected SQLite driver. mattn registers as "sqlite3";
// modernc registers as "sqlite".
const DriverName = "sqlite3"

// AppendConnectionPragmas encodes agentsview's shared connection
// pragmas into params using the `_`-prefixed DSN query params that
// mattn/go-sqlite3 recognizes.
func AppendConnectionPragmas(params url.Values, readOnly bool, cacheSizeKiB int) {
	params.Set("_busy_timeout", "5000")
	params.Set("_foreign_keys", "ON")
	params.Set("_cache_size", strconv.Itoa(cacheSizeKiB))
	if !readOnly {
		params.Set("_journal_mode", "WAL")
		params.Set("_synchronous", "NORMAL")
	}
}
