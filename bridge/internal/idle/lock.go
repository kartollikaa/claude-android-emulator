package idle

import (
	"errors"
	"os"
)

// ErrLocked is returned when another watcher holds the lock.
var ErrLocked = errors.New("another watcher holds the lock")

// Lock takes the lock file at path for as long as this process holds it; the system drops it when the process dies.
func Lock(path string) (release func(), err error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, err
	}

	return func() {
		file.Close()
		os.Remove(path)
	}, nil
}
