package idle

import (
	"errors"
	"os"
)

// ErrLocked is returned when another watcher holds the lock.
var ErrLocked = errors.New("another watcher holds the lock")

// Lock takes the lock file at path for as long as this process holds it; the system drops it when the process dies.
func Lock(path string) (release func(), err error) {
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return nil, err
		}
		if err := lockFile(file); err != nil {
			file.Close()
			return nil, err
		}
		// A holder removes the file before letting go, so a lock on a file no longer at path guards nothing.
		if isAtPath(file, path) {
			return func() {
				// Windows refuses to remove an open file; there another opener in turn keeps it from going.
				isRemoved := os.Remove(path) == nil
				file.Close()
				if !isRemoved {
					os.Remove(path)
				}
			}, nil
		}
		file.Close()
	}
}

func isAtPath(file *os.File, path string) bool {
	opened, err := file.Stat()
	if err != nil {
		return false
	}
	named, err := os.Stat(path)

	return err == nil && os.SameFile(opened, named)
}
