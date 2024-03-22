package internal

import (
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
			LogDebug("skipping unmatched file name %s", path)
			return nil
		}
		fn(path, info)
		return nil
	})
}
