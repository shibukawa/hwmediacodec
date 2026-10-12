//go:build !js

package fatal

import "log"

// Exit prints err and ends the program with a failure status.
func Exit(err error) {
	log.Fatal(err)
}
