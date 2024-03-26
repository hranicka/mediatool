package internal

import (
	"bufio"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	filePattern = regexp.MustCompile(`(?i)\.mkv$`)
)

func FileIgnoresWhenExist(path string) []string {
	ignores, err := FileIgnores(path)
	if err != nil {
		return nil
	}
	return ignores
}

func FileIgnores(path string) (ignores []string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		text := scanner.Text()
		if text == "" {
			continue
		}
		ignores = append(ignores, text)
	}
	if err = scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading from file: %w", err)
	}

	slog.Info("loaded global ignores", "path", path, "ignores", ignores)
	return ignores, nil
}

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
