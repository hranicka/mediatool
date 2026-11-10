// Package ignore loads and matches the dot ignore files guarding the media
// library: one plain substring per line, matched literally against the full
// path (directories and file names both count), mediatool semantics.
package ignore

import (
	"bufio"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// sharedFiles are read for every subcommand.
var sharedFiles = []string{".mediatool-ignore"}

// commandFiles maps subcommands to their own ignore file: a per-command
// ignore list, so a path can be excluded from one command (e.g. "not to
// clean") while still being processed by the others (e.g. re-encode).
var commandFiles = map[string]string{
	"convert": ".hevcconverter-ignore",
	"clean":   ".cleaner-ignore",
	"ac3":     ".ac3converter-ignore",
	"enhance": ".enhance-ignore",
	"dup":     ".dupfinder-ignore",
}

// List is a set of substring entries matched literally against full paths.
type List struct {
	entries []string
}

// Load returns the ignore list for a subcommand: the shared files plus the
// command's own file, looked up in dir **and every ancestor directory up to
// the filesystem root** (like a flat gitignore hierarchy), plus the extra
// CLI entries. Missing files are the normal case.
func Load(dir, tool string, extra []string) List {
	names := append([]string{}, sharedFiles...)
	if legacy, ok := commandFiles[tool]; ok {
		names = append(names, legacy)
	}

	l := List{}
	for d := dir; ; d = filepath.Dir(d) {
		for _, name := range names {
			l.readFile(filepath.Join(d, name))
		}
		if d == filepath.Dir(d) {
			break // reached the root
		}
	}
	l.entries = append(l.entries, extra...)
	slog.Debug("ignore list", "tool", tool, "dir", dir, "entries", l.entries)
	return l
}

func (l *List) readFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		l.entries = append(l.entries, line)
	}
}

// Match reports whether any entry occurs in the path (literal substring,
// no globbing).
func (l List) Match(path string) bool {
	for _, e := range l.entries {
		if strings.Contains(path, e) {
			return true
		}
	}
	return false
}

// Len returns the number of entries.
func (l List) Len() int { return len(l.entries) }
