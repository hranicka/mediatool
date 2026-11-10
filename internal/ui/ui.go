// Package ui renders mediatool's terminal output: plain text when the
// streams are redirected (the automation contract, byte-compatible with
// the pre-ui output), colored and progress-aware when both stdout and
// stderr are terminals.
package ui

import (
	"os"
	"strings"
)

// Profile describes how output should be rendered.
type Profile struct {
	Interactive bool // stdout and stderr are both terminals
	Color       bool // ANSI color codes allowed
	UTF8        bool // unicode glyphs allowed
	Width       int  // terminal width in columns (80 when unknown)
}

// Detect inspects the output streams and the environment. NO_COLOR,
// CLICOLOR_FORCE and TERM=dumb are honored per convention.
func Detect(stdout, stderr *os.File) Profile {
	p := Profile{
		Interactive: isTerminal(stdout) && isTerminal(stderr),
		Width:       80,
	}
	if w := terminalWidth(stdout); w > 0 {
		p.Width = w
	}
	switch {
	case os.Getenv("NO_COLOR") != "":
		p.Color = false
	case forceColor():
		p.Color = true
	default:
		p.Color = p.Interactive && os.Getenv("TERM") != "dumb"
	}
	p.UTF8 = hasUTF8Locale()
	return p
}

// isTerminal reports whether f is attached to a character device (a
// terminal, but also /dev/null, which is harmless to pretty-print into).
func isTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func forceColor() bool {
	v := os.Getenv("CLICOLOR_FORCE")
	return v != "" && v != "0"
}

func hasUTF8Locale() bool {
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if s := os.Getenv(v); s != "" {
			return strings.Contains(strings.ToLower(s), "utf-8")
		}
	}
	return false
}
