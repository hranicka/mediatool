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

// SwapPair moves both inputs to their .old backups and newFile to dest,
// rolling back on any failure. It returns the reference's backup path.
func SwapPair(a, b, newFile, dest string) (string, error) {
	if a == b {
		return "", fmt.Errorf("reference and dub are the same file: %s", a)
	}
	oldA, oldB := a+".old", b+".old"
	if _, err := os.Stat(oldA); err == nil {
		return "", fmt.Errorf("backup %s already exists", oldA)
	}
	if _, err := os.Stat(oldB); err == nil {
		return "", fmt.Errorf("backup %s already exists", oldB)
	}
	if err := os.Rename(a, oldA); err != nil {
		return "", fmt.Errorf("cannot rename the reference: %w", err)
	}
	if err := os.Rename(b, oldB); err != nil {
		if rbErr := os.Rename(oldA, a); rbErr != nil {
			return "", fmt.Errorf("cannot rename the dub: %w (rollback also failed: %v - reference preserved as %s)", err, rbErr, oldA)
		}
		return "", fmt.Errorf("cannot rename the dub: %w", err)
	}
	if err := os.Rename(newFile, dest); err != nil {
		rbB := os.Rename(oldB, b)
		rbA := os.Rename(oldA, a)
		if rbA != nil || rbB != nil {
			return "", fmt.Errorf("cannot rename the aligned file: %w (rollback failures: reference %v, dub %v - originals preserved as %s and %s)", err, rbA, rbB, oldA, oldB)
		}
		return "", fmt.Errorf("cannot rename the aligned file: %w", err)
	}
	return oldA, nil
}

// ReplacePair puts newFile in place of a and removes b outright - the
// -no-backup path for a two-input command. The aligned file is in place
// before the originals are removed, so a failure cannot lose the result.
func ReplacePair(a, b, newFile, dest string) error {
	if a == b {
		return fmt.Errorf("reference and dub are the same file: %s", a)
	}
	if err := os.Rename(newFile, dest); err != nil {
		return fmt.Errorf("cannot rename the aligned file: %w", err)
	}
	if dest != a {
		if err := os.Remove(a); err != nil {
			return fmt.Errorf("aligned file is in place at %s, but the original reference could not be removed: %w", dest, err)
		}
	}
	if err := os.Remove(b); err != nil {
		return fmt.Errorf("aligned file is in place at %s, but the dubbed source could not be removed: %w", dest, err)
	}
	return nil
}
