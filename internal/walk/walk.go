// Package walk discovers media files under a directory.
package walk

import (
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
)

// Extensions lists the media file extensions the walk picks up: the
// library containers (mkv, mp4) plus the camera/dump formats the retired
// bash hevc-convert.sh handled (.mts, .avi, .mpg, ...).
var Extensions = []string{".mkv", ".mp4", ".avi", ".mpg", ".mpeg", ".mts", ".m2ts", ".mov", ".ts", ".wmv"}

// IsMedia reports whether a file extension (compared case-insensitively)
// is a media extension the walk picks up.
func IsMedia(ext string) bool {
	ext = strings.ToLower(ext)
	for _, e := range Extensions {
		if ext == e {
			return true
		}
	}
	return false
}

// Media returns the paths of media files under dir (case-insensitive on
// the extensions), excluding AppleDouble ._ files; .old backups never
// match because the path must end in a media extension. The result is
// sorted for a deterministic order. Unreadable subdirectories are logged
// and skipped, not fatal (like find); only an unreadable root fails.
func Media(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			slog.Warn("skipping unreadable path", "path", path, "err", err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if strings.HasPrefix(base, "._") {
			return nil
		}
		if IsMedia(filepath.Ext(path)) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("cannot walk %s: %w", dir, err)
	}
	sort.Strings(paths)
	return paths, nil
}
