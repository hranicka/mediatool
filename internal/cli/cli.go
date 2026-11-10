// Package cli holds the flag adapters and target resolution shared by the
// subcommands.
package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Ignores adapts a comma-separated flag value into a string slice. String
// tolerates the zero value: the flag package constructs one reflectively
// for -h output.
type Ignores struct{ Dst *[]string }

func (v Ignores) String() string {
	if v.Dst == nil {
		return ""
	}
	return strings.Join(*v.Dst, ",")
}

func (v Ignores) Set(s string) error {
	if v.Dst == nil {
		return nil
	}
	for _, e := range strings.Split(s, ",") {
		if e != "" {
			*v.Dst = append(*v.Dst, e)
		}
	}
	return nil
}

// ResolveMediaTarget resolves a positional argument to an absolute,
// symlink-resolved path. It must be an existing directory or, when
// allowFile is set, an existing MKV/MP4 file.
func ResolveMediaTarget(arg string, allowFile bool) (string, error) {
	target, err := filepath.Abs(arg)
	if err != nil {
		return "", fmt.Errorf("cannot resolve target: %w", err)
	}
	if resolved, linkErr := filepath.EvalSymlinks(target); linkErr == nil {
		target = resolved
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", fmt.Errorf("target %s does not exist", target)
	}
	if !info.IsDir() {
		if !allowFile {
			return "", fmt.Errorf("%s is not a directory", target)
		}
		switch strings.ToLower(filepath.Ext(target)) {
		case ".mkv", ".mp4":
		default:
			return "", fmt.Errorf("%s is neither a media file nor a directory", target)
		}
	}
	return target, nil
}

// IgnoresDir returns the directory whose dot ignore files govern the
// target: the target's own directory for a single file.
func IgnoresDir(target string) string {
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return filepath.Dir(target)
	}
	return target
}
