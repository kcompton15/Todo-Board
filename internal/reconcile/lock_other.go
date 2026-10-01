//go:build !darwin && !linux

package reconcile

import "errors"

var ErrLocked = errors.New("another reconciliation run holds the lock")

func Lock(string) (func(), error) {
	return nil, errors.New("reconciler locking supports macOS/Linux only")
}
