package internal

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
)

var (
	filePattern = regexp.MustCompile(`(?i)\.mkv$`)
)

func Walk(dir string, fn func(path string, info os.FileInfo)) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if !filePattern.MatchString(path) {
			slog.Debug("skipping unmatched file name", "path", path)
			return nil
		}
		fn(path, info)
		return nil
	})
}
