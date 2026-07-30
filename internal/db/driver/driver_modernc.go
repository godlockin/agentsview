//go:build moderncsqlite

package driver

import (
	"net/url"
	"strconv"

	_ "modernc.org/sqlite"
)

// DriverName is the database/sql driver name used by the
// currently-selected SQLite driver. modernc registers as "sqlite".
const DriverName = "sqlite"

// AppendConnectionPragmas encodes agentsview's shared connection
// pragmas into params using the syntax modernc.org/sqlite expects.
//
// Unlike mattn/go-sqlite3, modernc does not honor the `_busy_timeout`,
// `_foreign_keys`, `_cache_size`, `_journal_mode`, or `_synchronous`
// DSN query params. It runs any statement passed via a repeated
// `_pragma=<name>(<value>)` parameter after opening the connection,
// so the pragmas must be encoded that way or they silently take no
// effect (foreign keys stay OFF, the journal stays in rollback mode,
// and the cache size stays at the -2000 default).
func AppendConnectionPragmas(params url.Values, readOnly bool, cacheSizeKiB int) {
	params.Add("_pragma", "busy_timeout(5000)")
	params.Add("_pragma", "foreign_keys(ON)")
	params.Add("_pragma", "cache_size("+strconv.Itoa(cacheSizeKiB)+")")
	if !readOnly {
		params.Add("_pragma", "journal_mode(WAL)")
		params.Add("_pragma", "synchronous(NORMAL)")
	}
}
