// Package run executes external commands with injectable runners and
// locates the ffmpeg/ffprobe binaries.
package run

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Runner executes external commands. Implementations capture stdout and
// stderr; on a non-zero exit the returned error must contain the stderr
// text.
type Runner interface {
	// Output runs the command and returns its stdout.
	Output(name string, args ...string) ([]byte, error)
	// Stream runs the command, passing stderr through to the process's
	// stderr (progress of long encodes) while also capturing it, and
	// returns the captured stderr.
	Stream(name string, args ...string) ([]byte, error)
}

// Cmd is the real Runner backed by os/exec.
type Cmd struct{}

func (Cmd) Output(name string, args ...string) ([]byte, error) {
	var out, errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, errBuf.String())
	}
	return out.Bytes(), nil
}

func (Cmd) Stream(name string, args ...string) ([]byte, error) {
	var errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.MultiWriter(os.Stderr, &errBuf)
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%v: %s", err, errBuf.String())
	}
	return errBuf.Bytes(), nil
}

// Locate returns the ffmpeg and ffprobe binary paths: FFMPEG_BIN overrides
// ffmpeg (FFPROBE_BIN overrides ffprobe, otherwise a sibling of the chosen
// ffmpeg is preferred), otherwise Jellyfin's build, common install locations
// and PATH are searched.
func Locate() (ffmpeg, ffprobe string, err error) {
	if p := os.Getenv("FFMPEG_BIN"); p != "" {
		if info, statErr := os.Stat(p); statErr != nil || info.IsDir() {
			return "", "", fmt.Errorf("FFMPEG_BIN=%s is not an executable file", p)
		}
		ffmpeg = p
	} else {
		for _, cand := range []string{
			"jellyfin-ffmpeg", "/usr/lib/jellyfin-ffmpeg/ffmpeg",
			filepath.Join(os.Getenv("HOME"), ".local", "bin", "ffmpeg"),
			"/usr/local/bin/ffmpeg", "/snap/bin/ffmpeg", "ffmpeg",
		} {
			if found, ok := lookPath(cand); ok {
				ffmpeg = found
				break
			}
		}
		if ffmpeg == "" {
			return "", "", errors.New("no ffmpeg found (searched PATH, /usr/lib/jellyfin-ffmpeg, ~/.local/bin, /usr/local/bin, /snap/bin); install it or set FFMPEG_BIN")
		}
	}

	if p := os.Getenv("FFPROBE_BIN"); p != "" {
		ffprobe = p
	} else if dir := filepath.Dir(ffmpeg); strings.ContainsRune(ffmpeg, os.PathSeparator) && dir != "." {
		sibling := filepath.Join(dir, "ffprobe")
		if _, statErr := os.Stat(sibling); statErr == nil {
			ffprobe = sibling
		}
	}
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	return ffmpeg, ffprobe, nil
}

func lookPath(cand string) (string, bool) {
	if strings.ContainsRune(cand, '/') {
		info, err := os.Stat(cand)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			return "", false
		}
		return cand, true
	}
	p, err := exec.LookPath(cand)
	return p, err == nil
}
