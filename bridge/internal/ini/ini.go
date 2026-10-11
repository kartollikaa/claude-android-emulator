// Package ini reads the key=value files the emulator and the SDK write.
package ini

import "strings"

// Parse maps each key to its value; values may hold '=' themselves (tokens end in "==").
func Parse(text string) map[string]string {
	values := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\r"), "=")
		if ok && key != "" {
			values[strings.TrimSpace(key)] = strings.TrimSpace(value)
		}
	}

	return values
}
