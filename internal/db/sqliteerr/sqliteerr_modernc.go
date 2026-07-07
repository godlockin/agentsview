//go:build !mattnsqlite
// +build !mattnsqlite

package sqliteerr

import (
	"errors"

	"modernc.org/sqlite"
)

// As unwraps to *Error backed by modernc's *sqlite.Error. modernc
// stores the extended code (e.g. 2067 for UNIQUE) directly in
// Code(); primary and extended are the same value, so we mirror
// into both fields.
func As(err error) (*Error, bool) {
	var se *sqlite.Error
	if errors.As(err, &se) {
		c := se.Code()
		return &Error{
			Code:         c,
			ExtendedCode: c,
		}, true
	}
	return nil, false
}
