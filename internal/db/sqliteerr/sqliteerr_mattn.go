//go:build !moderncsqlite

package sqliteerr

import (
	"errors"

	"github.com/mattn/go-sqlite3"
)

// As unwraps an arbitrary error chain to *Error backed by the mattn
// driver's *sqlite3.Error.
func As(err error) (*Error, bool) {
	var se sqlite3.Error
	if errors.As(err, &se) {
		return &Error{
			Code:         int(se.Code),
			ExtendedCode: int(se.ExtendedCode),
		}, true
	}
	return nil, false
}
