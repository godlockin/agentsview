//go:build !mattnsqlite
// +build !mattnsqlite

package driver

import (
	_ "modernc.org/sqlite"
)

// DriverName is the database/sql driver name used by the
// currently-selected SQLite driver. modernc registers as "sqlite".
const DriverName = "sqlite"
