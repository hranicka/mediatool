package ui

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestDetectStreams(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	p := Detect(r, r)
	if p.Interactive || p.Color {
		t.Errorf("pipes must not be interactive/colored, got %+v", p)
	}
	if p.Width != 80 {
		t.Errorf("width = %d, want default 80", p.Width)
	}
}

func TestDetectColorEnv(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()

	t.Setenv("CLICOLOR_FORCE", "1")
	if p := Detect(r, r); !p.Color {
		t.Error("CLICOLOR_FORCE must force color on pipes")
	}

	t.Setenv("NO_COLOR", "1")
	if p := Detect(r, r); p.Color {
		t.Error("NO_COLOR must disable color even with CLICOLOR_FORCE")
	}
}

func TestDetectUTF8Locale(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()

	t.Setenv("CLICOLOR_FORCE", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_CTYPE", "")

	t.Setenv("LANG", "en_US.UTF-8")
	if p := Detect(r, r); !p.UTF8 {
		t.Error("LANG=utf-8 must enable unicode symbols")
	}

	t.Setenv("LANG", "C")
	if p := Detect(r, r); p.UTF8 {
		t.Error("LANG=C must fall back to ASCII symbols")
	}
}

func TestPalette(t *testing.T) {
	on := Palette{true}
	if got := on.Good("x"); got != "\x1b[32mx\x1b[0m" {
		t.Errorf("Good = %q", got)
	}
	off := Palette{false}
	if got := off.Good("x"); got != "x" {
		t.Errorf("disabled Good = %q", got)
	}
}

func TestSymbols(t *testing.T) {
	u := newSymbols(true)
	if u.Done != "✓" || u.Skip != "–" || u.Fail != "✗" {
		t.Errorf("unicode symbols wrong: %+v", u)
	}
	a := newSymbols(false)
	if a.Done != "+" || a.Skip != "-" || a.Fail != "x" || a.Arrow != "->" {
		t.Errorf("ascii symbols wrong: %+v", a)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("abcdefghij", 20, true); got != "abcdefghij" {
		t.Errorf("no-op truncate = %q", got)
	}
	if got := truncate("abcdefghij", 5, true); got != "abcd…" {
		t.Errorf("utf8 truncate = %q", got)
	}
	if got := truncate("abcdefghij", 5, false); got != "ab..." {
		t.Errorf("ascii truncate = %q", got)
	}
}

func TestClockAndSpan(t *testing.T) {
	if got := clock(50 * time.Second); got != "0:00:50" {
		t.Errorf("clock(50s) = %q", got)
	}
	if got := clock(100 * time.Second); got != "0:01:40" {
		t.Errorf("clock(100s) = %q", got)
	}
	for d, want := range map[time.Duration]string{
		45 * time.Second:   "45s",
		192 * time.Second:  "3m12s",
		4353 * time.Second: "1h 12m 33s",
	} {
		if got := span(d); got != want {
			t.Errorf("span(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestBar(t *testing.T) {
	rep := New(nil, Profile{UTF8: true})
	if got := rep.bar(0.5, 20); got != strings.Repeat("█", 10)+strings.Repeat("░", 10) {
		t.Errorf("bar(0.5) = %q", got)
	}
	if got := rep.bar(2, 4); got != "████" {
		t.Errorf("bar(2) must clamp = %q", got)
	}
	rep.p.UTF8 = false
	if got := rep.bar(0.5, 4); got != "##--" {
		t.Errorf("ascii bar = %q", got)
	}
}
