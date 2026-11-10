// Package backup swaps converted files in place keeping a .old backup.
package backup

import (
	"fmt"
	"os"
)

// Swap moves src to src.old and newFile to src, never clobbering an
// existing backup. On failure the original is rolled back so nothing is
// lost. Returns the backup path.
func Swap(src, newFile string) (string, error) {
	old := src + ".old"
	if _, err := os.Stat(old); err == nil {
		return "", fmt.Errorf("backup %s already exists", old)
	}
	if err := os.Rename(src, old); err != nil {
		return "", fmt.Errorf("cannot rename source file: %w", err)
	}
	if err := os.Rename(newFile, src); err != nil {
		if rbErr := os.Rename(old, src); rbErr != nil {
			return "", fmt.Errorf("cannot rename converted file: %w (rollback also failed: %v - original preserved as %s)", err, rbErr, old)
		}
		return "", fmt.Errorf("cannot rename converted file: %w", err)
	}
	return old, nil
}
