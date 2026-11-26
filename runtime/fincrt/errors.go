package fincrt

import "errors"

// Sentinel errors of the FinCore standard library (spec 10.1). They are
// distinct values and must be compared with ==.
var (
	ErrEOF       = errors.New("fincore: EOF")
	ErrTimeout   = errors.New("fincore: timeout")
	ErrCancelled = errors.New("fincore: cancelled")
	ErrZeroWrite = errors.New("fincore: zero-byte write")
	ErrClosed    = errors.New("fincore: use of closed resource")
	ErrIO        = errors.New("fincore: I/O error")
	ErrNotFound  = errors.New("fincore: not found")
	ErrMaxDepth  = errors.New("fincore: maximum depth reached")
	ErrArenaFull = errors.New("fincore: arena full")
	ErrExec      = errors.New("fincore: exec failed")
	// ErrParse is an extension to the spec: returned by ParseUint on
	// malformed or overflowing input.
	ErrParse = errors.New("fincore: invalid number")
)
