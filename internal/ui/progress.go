package ui

import (
	"fmt"
	"strings"
	"time"
)

const barBlocks = 20

// bar renders a progress bar of the given block width for fraction frac
// (clamped to 0..1).
func (r *Reporter) bar(frac float64, width int) string {
	if width < 1 {
		width = 1
	}
	if frac < 0 {
		frac = 0
	} else if frac > 1 {
		frac = 1
	}
	full, empty := "█", "░"
	if !r.p.UTF8 {
		full, empty = "#", "-"
	}
	filled := int(frac * float64(width))
	return strings.Repeat(full, filled) + strings.Repeat(empty, width-filled)
}

// truncate shortens s to at most max runes, replacing the cut with an
// ellipsis.
func truncate(s string, max int, utf8 bool) string {
	runes := []rune(s)
	if max <= 0 || len(runes) <= max {
		return s
	}
	ell := "…"
	if !utf8 {
		ell = "..."
	}
	n := len([]rune(ell))
	if max <= n {
		return ell
	}
	return string(runes[:max-n]) + ell
}

// clock formats a duration as H:MM:SS for progress displays.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds())
	return fmt.Sprintf("%d:%02d:%02d", s/3600, (s/60)%60, s%60)
}

// span formats a duration for summaries (45s, 3m12s, 1h 12m 33s).
func span(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d.Seconds() + 0.5)
	switch {
	case s < 60:
		return fmt.Sprintf("%ds", s)
	case s < 3600:
		return fmt.Sprintf("%dm%02ds", s/60, s%60)
	default:
		return fmt.Sprintf("%dh %02dm %02ds", s/3600, (s/60)%60, s%60)
	}
}
