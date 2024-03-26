package internal

import (
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	filePattern = regexp.MustCompile(`(?i)\.mkv$`)
)

func Walk(dir string, ignores []string, fn func(path string, info os.FileInfo)) {
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if !filePattern.MatchString(path) {
			slog.Debug("skipping unmatched file name", "path", path)
			return nil
		}

		for _, ignore := range ignores {
			if strings.Contains(path, ignore) {
				slog.Info("skipping ignored file", "path", path, "ignore", ignore)
				return nil
			}
		}

		fn(path, info)
		return nil
	})
}
