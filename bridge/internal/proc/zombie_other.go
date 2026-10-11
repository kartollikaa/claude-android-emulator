//go:build unix && !linux && !darwin

package proc

func isZombie(int) bool { return false }
