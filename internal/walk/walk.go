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

// Media returns the paths of *.mkv and *.mp4 files under dir
// (case-insensitive), excluding AppleDouble ._ files; .old backups never
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
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mkv", ".mp4":
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
