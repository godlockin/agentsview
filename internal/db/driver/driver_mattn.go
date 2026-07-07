//go:build !moderncsqlite
// +build !moderncsqlite

// Package driver selects the underlying SQLite driver via build
// tags. The mattn driver (CGO) is the default; modernc.org/sqlite
// (pure Go) takes over when the build tag `moderncsqlite` is set.
// Call sites use DriverName to open connections so they don't
// have to know which driver is active.
package driver

import (
	_ "github.com/mattn/go-sqlite3"
)

// DriverName is the database/sql driver name used by the
// currently-selected SQLite driver. mattn registers as "sqlite3";
// modernc registers as "sqlite".
const DriverName = "sqlite3"
