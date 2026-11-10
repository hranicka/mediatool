package run

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ProgressRunner is the optional Runner capability of parsing ffmpeg's
// -stats output instead of echoing it: stderr is only captured, and every
// parsed stats update is reported to the callback.
type ProgressRunner interface {
	Progress(name string, onStats func(elapsed time.Duration, speed string), args ...string) ([]byte, error)
}

// StreamProgress runs name with args, preferring the stats-aware
// ProgressRunner (no stderr echo, onStats updates) when the runner
// supports it and onStats is set, and falling back to Stream (stderr
// passthrough) otherwise. It returns the captured stderr either way.
func StreamProgress(r Runner, name string, onStats func(elapsed time.Duration, speed string), args ...string) ([]byte, error) {
	if pr, ok := r.(ProgressRunner); ok && onStats != nil {
		return pr.Progress(name, onStats, args...)
	}
	return r.Stream(name, args...)
}

// Progress implements the capability on the real runner.
func (Cmd) Progress(name string, onStats func(elapsed time.Duration, speed string), args ...string) ([]byte, error) {
	var errBuf bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = &statsWriter{buf: &errBuf, on: onStats}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, errBuf.String())
	}
	return errBuf.Bytes(), nil
}

var (
	statsTimeRe  = regexp.MustCompile(`time=(\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)
	statsSpeedRe = regexp.MustCompile(`speed=\s*([\d.]+)x`)
)

// statsWriter captures stderr and extracts ffmpeg -stats updates
// (\r-separated "frame= ... time=... speed=..." blocks) for the callback.
type statsWriter struct {
	buf  *bytes.Buffer
	on   func(elapsed time.Duration, speed string)
	tail []byte
}

func (w *statsWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	w.tail = append(w.tail, p...)
	for {
		i := bytes.IndexAny(w.tail, "\r\n")
		if i < 0 {
			break
		}
		w.emit(w.tail[:i])
		w.tail = w.tail[i+1:]
	}
	// stats segments are short; drop unreasonably long leftovers
	if len(w.tail) > 4096 {
		w.tail = nil
	}
	return len(p), nil
}

func (w *statsWriter) emit(seg []byte) {
	if w.on == nil {
		return
	}
	m := statsTimeRe.FindSubmatch(seg)
	if m == nil {
		return
	}
	h, _ := strconv.Atoi(string(m[1]))
	mm, _ := strconv.Atoi(string(m[2]))
	sec, _ := strconv.ParseFloat(string(m[3]), 64)
	d := time.Duration(h)*time.Hour + time.Duration(mm)*time.Minute + time.Duration(sec*float64(time.Second))
	speed := ""
	if sm := statsSpeedRe.FindSubmatch(seg); sm != nil {
		speed = string(sm[1]) + "x"
	}
	w.on(d, speed)
}

// LastErrLine returns the last line of captured output that is not part
// of an ffmpeg -stats progress block, for error reporting. It returns ""
// when the output holds nothing but progress lines.
func LastErrLine(out []byte) string {
	var last string
	for _, seg := range bytes.FieldsFunc(out, func(r rune) bool { return r == '\r' || r == '\n' }) {
		line := strings.TrimSpace(string(seg))
		if line == "" || isStatsLine(line) {
			continue
		}
		last = line
	}
	return last
}

func isStatsLine(line string) bool {
	for _, prefix := range []string{"frame=", "size=", "Lsize=", "video:", "audio:", "subtitle:"} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
