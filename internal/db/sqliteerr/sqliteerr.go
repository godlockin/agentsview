// Package sqliteerr exposes the small surface of the active SQLite
// driver's error type that the db package needs to classify errors
// (primary-code matching). The mattn driver is the default; the
// modernc driver takes over when the build tag `moderncsqlite` is
// set. Call sites in the db package import this package and use
// its Error type and constants instead of importing the underlying
// driver package directly, so the db package compiles under both
// drivers.
//
// The constants here are the SQLite protocol's primary and
// extended result codes (https://www.sqlite.org/rescode.html).
// They are integer values fixed by the SQLite C API and identical
// for mattn and modernc.
package sqliteerr

// SQLite primary and extended result codes used by the db
// package's error classifiers.
const (
	// ErrError (SQLITE_ERROR = 1) covers malformed FTS queries.
	ErrError = 1

	// ErrConstraintUnique is the value mattn's
	// Error.ExtendedCode carries for a UNIQUE violation
	// (SQLITE_CONSTRAINT_UNIQUE = 2067, which the driver
	// flattens to its integer form on the error struct).
	ErrConstraintUnique = 2067

	// ErrConstraintUniquePrimary is the SQLITE_CONSTRAINT
	// primary result code (19). modernc.org/sqlite flattens
	// primary and extended codes into a single Code() int, so
	// modernc-reported UNIQUE failures also carry 2067, not 19.
	ErrConstraintUniquePrimary = 19
)

// Error is the minimum shape the db package reads. Code is the
// primary result code; ExtendedCode is the extended result code if
// the driver distinguishes them. modernc.org/sqlite flattens both
// into a single int (Code()), in which case ExtendedCode equals
// Code.
type Error struct {
	Code         int
	ExtendedCode int
}
