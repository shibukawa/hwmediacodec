//go:build !((windows || darwin || linux) && !android && !ios)

package main

import "github.com/shibukawa/hwmediacodec/cmd/hwmediacodec/checker"

// hasWindow is false where the command is built without Ebitengine
// (Android, iOS and the other systems): check runs the automatic checks
// only. The mobile sample has the window there.
const hasWindow = false

func checkWindow(r *checker.Report, _ checkOptions) int {
	panic("unreachable: check has no window on this platform")
}
