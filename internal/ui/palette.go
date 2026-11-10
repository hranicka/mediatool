package ui

// Palette wraps text in ANSI color codes when enabled; every method
// returns its argument unchanged otherwise.
type Palette struct{ enabled bool }

const (
	ansiReset  = "\x1b[0m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiDim    = "\x1b[2m"
	ansiBold   = "\x1b[1m"
)

func (p Palette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

// Good tints success text green.
func (p Palette) Good(s string) string { return p.wrap(ansiGreen, s) }

// Bad tints failure text red.
func (p Palette) Bad(s string) string { return p.wrap(ansiRed, s) }

// Warn tints warning text yellow.
func (p Palette) Warn(s string) string { return p.wrap(ansiYellow, s) }

// Info tints progress text cyan.
func (p Palette) Info(s string) string { return p.wrap(ansiCyan, s) }

// Dim de-emphasizes text.
func (p Palette) Dim(s string) string { return p.wrap(ansiDim, s) }

// Bold emphasizes text.
func (p Palette) Bold(s string) string { return p.wrap(ansiBold, s) }

// Symbols holds the per-outcome glyphs: unicode when the locale allows
// it, ASCII fallbacks otherwise.
type Symbols struct {
	Done, Skip, Fail, Warn, Run, Plan, Clock, Arrow, Download string
}

func newSymbols(utf8 bool) Symbols {
	if utf8 {
		return Symbols{
			Done: "✓", Skip: "–", Fail: "✗", Warn: "⚠", Run: "▶",
			Plan: "◇", Clock: "⏱", Arrow: "→", Download: "⇣",
		}
	}
	return Symbols{
		Done: "+", Skip: "-", Fail: "x", Warn: "!", Run: ">",
		Plan: "o", Clock: "@", Arrow: "->", Download: "<",
	}
}
