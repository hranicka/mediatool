package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/hranicka/mediatool/internal/media"
)

// KV is one settings header row.
type KV struct{ Key, Value string }

// Pair builds a header row.
func Pair(key, value string) KV { return KV{Key: key, Value: value} }

// Failure is one failed file, listed in the pretty summary.
type Failure struct{ Path, Msg string }

// Outcome aggregates a run for the end-of-run summary.
type Outcome struct {
	Done, Skipped, Failed int
	BytesIn, BytesOut     int64
	Failures              []Failure
	Elapsed               time.Duration
}

// Tone selects the pretty tint of a Raw line; plain output ignores it.
type Tone int

const (
	Neutral Tone = iota
	DimTone
	InfoTone
	WarnTone
	BadTone
	GoodTone
)

// Reporter renders a command's events. With a zero Profile it emits the
// plain, byte-compatible text output; an interactive Profile adds colors,
// symbols and a rewritten in-place progress line. Counter bookkeeping
// stays with the caller.
type Reporter struct {
	out   io.Writer
	p     Profile
	pal   Palette
	sym   Symbols
	live  bool          // progress line currently on screen (no newline)
	total time.Duration // duration of the file being processed
}

// New builds a Reporter writing to out.
func New(out io.Writer, p Profile) *Reporter {
	return &Reporter{out: out, p: p, pal: Palette{p.Color}, sym: newSymbols(p.UTF8)}
}

// Interactive reports whether the pretty rendering applies.
func (r *Reporter) Interactive() bool { return r.p.Interactive }

// close erases an active live line (progress bar or quiet walking line)
// before any other output, so transient lines never litter the scrollback.
func (r *Reporter) close() {
	if r.live {
		fmt.Fprint(r.out, "\r\x1b[K")
		r.live = false
	}
}

func idx(i, n int) string {
	if n <= 1 {
		return ""
	}
	return fmt.Sprintf("[%d/%d] ", i, n)
}

func (r *Reporter) dash() string {
	if r.p.UTF8 {
		return "—"
	}
	return "--"
}

func (r *Reporter) minus() string {
	if r.p.UTF8 {
		return "−"
	}
	return "-"
}

// lowerFirst downcases the first rune, turning the plain summary's
// "Converted" into the pretty block's "converted".
func lowerFirst(s string) string {
	runes := []rune(s)
	if len(runes) == 0 {
		return s
	}
	return string(unicode.ToLower(runes[0])) + string(runes[1:])
}

// Header prints the settings block and the separator rule, keeping the
// historical "Label         : value" alignment in plain output.
func (r *Reporter) Header(kvs []KV) {
	r.close()
	for _, kv := range kvs {
		value := kv.Value
		if r.p.Interactive {
			if value == "DRY RUN" {
				value = r.pal.Warn(value)
			}
			fmt.Fprintf(r.out, "%s: %s\n", r.pal.Dim(fmt.Sprintf("%-14s", kv.Key)), value)
			continue
		}
		if value == "DRY RUN" {
			value = r.pal.Warn(value)
		}
		fmt.Fprintf(r.out, "%s: %s\n", r.pal.Dim(fmt.Sprintf("%-14s", kv.Key)), value)
	}
	if r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(strings.Repeat("─", 42)))
		return
	}
	fmt.Fprintln(r.out, r.pal.Dim(strings.Repeat("=", 42)))
}

// Found announces the discovered file count (interactive rendering only).
func (r *Reporter) Found(n int) {
	if !r.p.Interactive {
		return
	}
	r.close()
	fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("%d media file(s) found", n)))
}

// Skip reports a skipped file: "Skipping (reason): path" plain, a fully
// dim symbol line pretty.
func (r *Reporter) Skip(i, n int, path, reason string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("Skipping (%s): %s", reason, path)))
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s", r.sym.Skip, idx(i, n), path, r.dash(), reason)
	if !r.p.Verbose {
		// quiet: rewrite one live "walking" line per file instead of a
		// scroll of skips, so progress stays visible without the noise
		r.status(r.pal.Dim(truncate(line, r.p.Width, r.p.UTF8)))
		return
	}
	fmt.Fprintln(r.out, r.pal.Dim(line))
}

// SkipInline reports a skip whose historical plain rendering glues the
// path to the closing paren (kept byte-compatible).
func (r *Reporter) SkipInline(i, n int, path, reason string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("Skipping (%s)%s", reason, path)))
		return
	}
	r.Skip(i, n, path, reason)
}

// Notice reports a per-file decision the user should not miss (forced
// re-encodes): "<msg>: <path>" plain, a yellow symbol line pretty.
func (r *Reporter) Notice(i, n int, path, msg string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Warn(fmt.Sprintf("%s: %s", msg, path)))
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s", r.sym.Warn, idx(i, n), path, r.dash(), msg)
	fmt.Fprintln(r.out, r.pal.Warn(line))
}

// OK reports a file needing no action: "OK (<msg>): <path>" plain, a dim
// skip-shaped line pretty.
func (r *Reporter) OK(i, n int, path, msg string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("OK (%s): %s", msg, path)))
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s", r.sym.Skip, idx(i, n), path, r.dash(), msg)
	if !r.p.Verbose {
		r.status(r.pal.Dim(truncate(line, r.p.Width, r.p.UTF8)))
		return
	}
	fmt.Fprintln(r.out, r.pal.Dim(line))
}

// Would reports a dry-run action: "<verb>: <path> (<summary>)" plain,
// cyan with a plan marker pretty. Details render as indented sub-lines.
func (r *Reporter) Would(verb string, i, n int, path, summary string, details ...string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Info(fmt.Sprintf("%s: %s (%s)", verb, path, summary)))
		for _, d := range details {
			fmt.Fprintln(r.out, r.pal.Dim("  - "+d))
		}
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s (%s)", r.sym.Plan, idx(i, n), path, r.dash(), verb, summary)
	fmt.Fprintln(r.out, r.pal.Info(line))
	for _, d := range details {
		fmt.Fprintln(r.out, r.pal.Dim("  - "+d))
	}
}

// Start announces a file being processed and prints its detail lines
// (plan, HDR notes). total is the source duration driving the progress
// bar; 0 when unknown. size may be empty for commands without one.
func (r *Reporter) Start(verb string, i, n int, path, size string, total time.Duration, details ...string) {
	r.close()
	r.total = total
	if !r.p.Interactive {
		if size != "" {
			fmt.Fprintln(r.out, r.pal.Info(fmt.Sprintf("%s: %s (%s)", verb, path, size)))
		} else {
			fmt.Fprintln(r.out, r.pal.Info(fmt.Sprintf("%s: %s", verb, path)))
		}
		for _, d := range details {
			fmt.Fprintln(r.out, r.pal.Dim("  "+d))
		}
		return
	}
	line := r.pal.Info(r.sym.Run+" "+idx(i, n)) + " " + r.pal.Bold(path)
	if size != "" {
		line += " " + r.pal.Dim("("+size+")")
	}
	fmt.Fprintln(r.out, line)
	for _, d := range details {
		fmt.Fprintln(r.out, r.pal.Dim("  "+d))
	}
}

// Detail prints an indented sub-line (plan entries, track lists).
func (r *Reporter) Detail(format string, args ...any) {
	r.close()
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(r.out, r.pal.Dim("  "+msg))
}

// Progress redraws the in-place bar line for the file started last; a
// no-op outside interactive mode.
func (r *Reporter) Progress(elapsed time.Duration, speed string) {
	if !r.p.Interactive {
		return
	}
	if r.total > 0 {
		frac := float64(elapsed) / float64(r.total)
		pct := int(frac*100 + 0.5)
		line := fmt.Sprintf("  %s  %3d%%  %s/%s", r.bar(frac, barBlocks), min(pct, 100), clock(elapsed), clock(r.total))
		if speed != "" {
			line += "  " + speed
		}
		r.status(truncate(line, r.p.Width, r.p.UTF8))
		return
	}
	line := "  " + clock(elapsed) + " elapsed"
	if speed != "" {
		line += "  " + speed
	}
	r.status(truncate(line, r.p.Width, r.p.UTF8))
}

// DownloadProgress redraws the live line for a running byte download
// (total 0 = unknown); a no-op outside interactive mode.
func (r *Reporter) DownloadProgress(label string, done, total int64) {
	if !r.p.Interactive {
		return
	}
	line := "  " + r.sym.Download + " " + label + "  " + media.HumanBytes(done)
	if total > 0 {
		frac := float64(done) / float64(total)
		line = fmt.Sprintf("  %s %s  %s  %s/%s  %d%%", r.sym.Download, label, r.bar(frac, barBlocks), media.HumanBytes(done), media.HumanBytes(total), min(int(frac*100+0.5), 100))
	}
	r.status(truncate(line, r.p.Width, r.p.UTF8))
}

// status rewrites the live line in place.
func (r *Reporter) status(s string) {
	fmt.Fprintf(r.out, "\r\x1b[K%s", s)
	r.live = true
}

// Success closes the current file: "<verb>: <path> (<in> -> <out>,
// backup: <backupPath>)" plain, a green result line replacing the bar
// pretty. backupPath empty means the original was kept (no backup).
// took is the per-file duration.
func (r *Reporter) Success(verb, path string, inBytes, outBytes int64, backupPath string, took time.Duration) {
	if !r.p.Interactive {
		if backupPath != "" {
			fmt.Fprintln(r.out, r.pal.Good(fmt.Sprintf("%s: %s (%s -> %s, backup: %s)", verb, path, media.HumanBytes(inBytes), media.HumanBytes(outBytes), backupPath)))
			return
		}
		fmt.Fprintln(r.out, r.pal.Good(fmt.Sprintf("%s: %s (original kept)", verb, path)))
		return
	}
	r.successBar(inBytes, outBytes, took)
}

// SuccessNoBackup closes the current file for -no-backup runs: the plain
// rendering says "(no backup)" because the original was removed, not kept.
// The pretty rendering matches Success.
func (r *Reporter) SuccessNoBackup(verb, path string, inBytes, outBytes int64, took time.Duration) {
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Good(fmt.Sprintf("%s: %s (%s -> %s, no backup)", verb, path, media.HumanBytes(inBytes), media.HumanBytes(outBytes))))
		return
	}
	r.successBar(inBytes, outBytes, took)
}

// successBar renders the shared pretty result line: sizes, saved percent
// and per-file duration, replacing the progress bar.
func (r *Reporter) successBar(inBytes, outBytes int64, took time.Duration) {
	line := fmt.Sprintf("  %s %s %s %s", r.pal.Good(r.sym.Done), media.HumanBytes(inBytes), r.pal.Dim(r.sym.Arrow), media.HumanBytes(outBytes))
	if inBytes > 0 && outBytes > 0 && outBytes < inBytes {
		saved := 100 - int(outBytes*100/inBytes)
		line += r.pal.Good(fmt.Sprintf("  %s%d%%", r.minus(), saved))
	}
	if took > 0 {
		line += r.pal.Dim("  " + span(took))
	}
	r.status(line)
	fmt.Fprint(r.out, "\n")
	r.live = false
}

// Fail closes the current file with an error: "Error: <msg>" on the
// historical stdout stream plain, a red result line plus detail pretty.
func (r *Reporter) Fail(msg, detail string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Bad("Error: "+msg))
		return
	}
	fmt.Fprintf(r.out, "\r\x1b[K  %s %s\n", r.pal.Bad(r.sym.Fail), r.pal.Bad(msg))
	r.live = false
	if detail != "" {
		corner := "└─"
		if !r.p.UTF8 {
			corner = "+-"
		}
		fmt.Fprintf(r.out, "     %s %s\n", r.pal.Bad(corner), r.pal.Dim(detail))
	}
}

// Note prints a warning note: verbatim on stderr plain, an inline yellow
// line pretty.
func (r *Reporter) Note(msg string) {
	if !r.p.Interactive {
		fmt.Fprintln(os.Stderr, r.pal.Warn(msg))
		return
	}
	r.close()
	s := strings.TrimSpace(msg)
	s = strings.TrimPrefix(s, "note: ")
	s = strings.TrimPrefix(s, "warning: ")
	fmt.Fprintf(r.out, "  %s %s\n", r.pal.Warn(r.sym.Warn), r.pal.Warn(s))
}

// Attempt announces an encode attempt. Interactive rendering folds the
// first attempt into the progress bar.
func (r *Reporter) Attempt(enc, label string, i, n int) {
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("  encoding with %s (attempt %d/%d: %s)...", enc, i, n, label)))
		return
	}
	if i > 1 {
		r.close()
		fmt.Fprintln(r.out, r.pal.Dim(fmt.Sprintf("  %s attempt %d/%d: %s", r.sym.Run, i, n, label)))
	}
}

// AttemptFailed reports a failed encode attempt.
func (r *Reporter) AttemptFailed(label string, i, n int) {
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Warn(fmt.Sprintf("  attempt %d (%s) failed...", i, label)))
		return
	}
	r.close()
	fmt.Fprintln(r.out, r.pal.Warn(fmt.Sprintf("  attempt %d (%s) failed", i, label)))
}

// Raw prints a free-form line (banners, standalone notices): verbatim
// plain, tinted by tone pretty.
func (r *Reporter) Raw(tone Tone, format string, args ...any) {
	r.close()
	msg := fmt.Sprintf(format, args...)
	nl := ""
	if s, ok := strings.CutSuffix(msg, "\n"); ok {
		msg, nl = s, "\n"
	}
	var tinted string
	switch tone {
	case DimTone:
		tinted = r.pal.Dim(msg)
	case InfoTone:
		tinted = r.pal.Info(msg)
	case WarnTone:
		tinted = r.pal.Warn(msg)
	case BadTone:
		tinted = r.pal.Bad(msg)
	case GoodTone:
		tinted = r.pal.Good(msg)
	default:
		tinted = msg
	}
	fmt.Fprint(r.out, tinted+nl)
}

// Summary prints the separator rule and the end-of-run tally. Plain keeps
// the historical one-liner (skipLabel empty collapses to the single-count
// form used by dup); pretty renders a block with sizes, failures and
// elapsed time.
func (r *Reporter) Summary(lead, doneLabel, skipLabel string, o Outcome) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(strings.Repeat("=", 42)))
		var line string
		if skipLabel == "" {
			line = fmt.Sprintf("%s %s: %d.", lead, doneLabel, o.Done)
		} else {
			line = fmt.Sprintf("%s %s: %d, %s: %d, failed: %d.", lead, doneLabel, o.Done, skipLabel, o.Skipped, o.Failed)
		}
		if o.Failed > 0 {
			fmt.Fprintln(r.out, r.pal.Bad(line))
		} else {
			fmt.Fprintln(r.out, r.pal.Good(line))
		}
		return
	}

	rule := strings.Repeat("─", 42)
	if !r.p.UTF8 {
		rule = strings.Repeat("=", 42)
	}
	fmt.Fprintln(r.out, r.pal.Dim(rule))

	done := fmt.Sprintf("%s %d %s", r.pal.Good(r.sym.Done), o.Done, lowerFirst(doneLabel))
	if o.BytesIn > 0 && o.BytesOut > 0 {
		done += fmt.Sprintf("  %s %s %s", media.HumanBytes(o.BytesIn), r.pal.Dim(r.sym.Arrow), media.HumanBytes(o.BytesOut))
		if o.BytesOut < o.BytesIn {
			saved := 100 - int(o.BytesOut*100/o.BytesIn)
			done += r.pal.Good(fmt.Sprintf("  (%s%d%%)", r.minus(), saved))
		}
	}
	fmt.Fprintln(r.out, done)
	if skipLabel != "" && o.Skipped > 0 {
		fmt.Fprintf(r.out, "%s %d %s\n", r.pal.Dim(r.sym.Skip), o.Skipped, lowerFirst(skipLabel))
	}
	if o.Failed > 0 {
		fmt.Fprintf(r.out, "%s %d failed\n", r.pal.Bad(r.sym.Fail), o.Failed)
		for _, f := range o.Failures {
			fmt.Fprintf(r.out, "   %s %s %s %s\n", r.pal.Bad(r.sym.Fail), f.Path, r.pal.Dim(r.dash()), r.pal.Dim(f.Msg))
		}
	}
	if o.Elapsed > 0 {
		fmt.Fprintf(r.out, "%s %s\n", r.pal.Dim(r.sym.Clock), r.pal.Dim(span(o.Elapsed)))
	}
}
