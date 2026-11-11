// Package backup swaps converted files in place, keeping a .old backup
// (Swap/SwapTo) or removing the original outright (Replace/ReplaceTo).
package backup

import (
	"fmt"
	"os"
)

// Swap moves src to src.old and newFile to src, never clobbering an
// existing backup. On failure the original is rolled back so nothing is
// lost. Returns the backup path.
func Swap(src, newFile string) (string, error) {
	return SwapTo(src, newFile, src)
}

// Replace swaps newFile in place of src removing the original - no .old
// backup is created. When dest is a distinct name the original is only
// removed after the new file is safely in place.
func Replace(src, newFile string) error {
	return ReplaceTo(src, newFile, src)
}

// ReplaceTo is Replace with a distinct destination: newFile moves to dest
// (which may differ from src, e.g. when the output container changes the
// extension) and src is removed. The rename happens first, so a failure
// leaves the original untouched.
func ReplaceTo(src, newFile, dest string) error {
	if err := os.Rename(newFile, dest); err != nil {
		return fmt.Errorf("cannot rename converted file: %w", err)
	}
	if src == dest {
		return nil
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("cannot remove original %s (the new file is in place at %s): %w", src, dest, err)
	}
	return nil
}

// SwapTo is Swap with a distinct destination: src moves to src.old and
// newFile moves to dest (which may differ from src, e.g. when the output
// container changes the extension). The backup keeps the source name.
func SwapTo(src, newFile, dest string) (string, error) {
	old := src + ".old"
	if _, err := os.Stat(old); err == nil {
		return "", fmt.Errorf("backup %s already exists", old)
	}
	if err := os.Rename(src, old); err != nil {
		return "", fmt.Errorf("cannot rename source file: %w", err)
	}
	if err := os.Rename(newFile, dest); err != nil {
		if rbErr := os.Rename(old, src); rbErr != nil {
			return "", fmt.Errorf("cannot rename converted file: %w (rollback also failed: %v - original preserved as %s)", err, rbErr, old)
		}
		return "", fmt.Errorf("cannot rename converted file: %w", err)
	}
	return old, nil
}
