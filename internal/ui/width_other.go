//go:build !linux

package ui

import "os"

// terminalWidth is only implemented for linux terminals; elsewhere the
// default width applies.
func terminalWidth(*os.File) int { return 0 }
