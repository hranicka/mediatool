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

// sharedFiles are read for every subcommand; the second name keeps the
// bash-suite era files working.
var sharedFiles = []string{".mediatool-ignore", ".video-scripts-ignore"}

// legacyFiles maps subcommands to their per-tool ignore file from the
// retired mediatool/bash generations.
var legacyFiles = map[string]string{
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
// tool's own file at the top of dir, plus the extra CLI entries. Missing
// files are the normal case.
func Load(dir, tool string, extra []string) List {
	names := append([]string{}, sharedFiles...)
	if legacy, ok := legacyFiles[tool]; ok {
		names = append(names, legacy)
	}

	l := List{}
	for _, name := range names {
		l.readFile(filepath.Join(dir, name))
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
