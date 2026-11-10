// Package ignore loads and matches the dot ignore files guarding the media
// library: one plain substring per line, matched literally against the full
// path (directories and file names both count), mediatool semantics.
package ignore

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// sharedFiles are read for every subcommand.
var sharedFiles = []string{".mediatool-ignore"}

// commandFiles are the per-command ignore lists: a path can be excluded
// from one command (e.g. "not to clean") while still being processed by
// the others (e.g. re-encode). dup is absent on purpose — it is a
// read-only report, so only the shared file and -i apply.
var commandFiles = map[string]string{
	"convert": ".mediatool-convert-ignore",
	"clean":   ".mediatool-clean-ignore",
	"ac3":     ".mediatool-ac3-ignore",
	"enhance": ".mediatool-enhance-ignore",
}

// legacyCommandFiles are the retired pre-2.0 per-command names, kept for
// back compatibility; every hit produces a rename warning.
var legacyCommandFiles = map[string]string{
	"convert": ".hevcconverter-ignore",
	"clean":   ".cleaner-ignore",
	"ac3":     ".ac3converter-ignore",
	"enhance": ".enhance-ignore",
}

// List is a set of substring entries matched literally against full paths.
type List struct {
	entries []string
}

// Load returns the ignore list for a subcommand: the shared file plus the
// command's own file (new and legacy names), looked up in dir **and every
// ancestor directory up to the filesystem root** (like a flat gitignore
// hierarchy), plus the extra CLI entries. Missing files are the normal
// case; the second return value holds one deprecation warning per legacy
// file found.
func Load(dir, tool string, extra []string) (List, []string) {
	type nameKind struct {
		name   string
		legacy bool
	}
	names := []nameKind{{sharedFiles[0], false}}
	if cmd, ok := commandFiles[tool]; ok {
		names = append(names, nameKind{cmd, false})
	}
	if cmd, ok := legacyCommandFiles[tool]; ok {
		names = append(names, nameKind{cmd, true})
	}

	l := List{}
	var warnings []string
	for d := dir; ; d = filepath.Dir(d) {
		for _, nk := range names {
			path := filepath.Join(d, nk.name)
			if l.readFile(path) && nk.legacy {
				warnings = append(warnings, fmt.Sprintf(
					"deprecated ignore file %s found - rename it to %s",
					path, filepath.Join(d, commandFiles[tool])))
			}
		}
		if d == filepath.Dir(d) {
			break // reached the root
		}
	}
	l.entries = append(l.entries, extra...)
	slog.Debug("ignore list", "tool", tool, "dir", dir, "entries", l.entries)
	return l, warnings
}

// readFile loads one ignore file; reports whether it exists.
func (l *List) readFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
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
	return true
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
