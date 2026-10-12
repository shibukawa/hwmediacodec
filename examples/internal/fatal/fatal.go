// Package fatal ends a sample program with an error message where the
// person in front of it sees it: on the terminal for a desktop program, in
// the page for one that runs in a browser, where the console is out of
// sight and an exited program leaves a blank canvas.
package fatal

import "strings"

// wrap breaks text into lines of at most width characters at spaces.
func wrap(text string, width int) string {
	var lines []string
	for _, paragraph := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				lines = append(lines, line)
				line = word
			}
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
