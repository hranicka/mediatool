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

// close terminates an active progress line before any other output.
func (r *Reporter) close() {
	if r.live {
		fmt.Fprint(r.out, "\n")
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
		fmt.Fprintf(r.out, "%-14s: %s\n", kv.Key, value)
	}
	if r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim(strings.Repeat("─", 42)))
		return
	}
	fmt.Fprintln(r.out, strings.Repeat("=", 42))
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
		fmt.Fprintf(r.out, "Skipping (%s): %s\n", reason, path)
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s", r.sym.Skip, idx(i, n), path, r.dash(), reason)
	fmt.Fprintln(r.out, r.pal.Dim(line))
}

// Notice reports a per-file decision the user should not miss (forced
// re-encodes): "<msg>: <path>" plain, a yellow symbol line pretty.
func (r *Reporter) Notice(i, n int, path, msg string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintf(r.out, "%s: %s\n", msg, path)
		return
	}
	line := fmt.Sprintf("%s %s%s %s %s", r.sym.Warn, idx(i, n), path, r.dash(), msg)
	fmt.Fprintln(r.out, r.pal.Warn(line))
}

// Would reports a dry-run action: "<verb>: <path> (<summary>)" plain,
// cyan with a plan marker pretty. Details render as indented sub-lines.
func (r *Reporter) Would(verb string, i, n int, path, summary string, details ...string) {
	r.close()
	if !r.p.Interactive {
		fmt.Fprintf(r.out, "%s: %s (%s)\n", verb, path, summary)
		for _, d := range details {
			fmt.Fprintf(r.out, "  - %s\n", d)
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
// bar; 0 when unknown.
func (r *Reporter) Start(verb string, i, n int, path, size string, total time.Duration, details ...string) {
	r.close()
	r.total = total
	if !r.p.Interactive {
		fmt.Fprintf(r.out, "%s: %s (%s)\n", verb, path, size)
		for _, d := range details {
			fmt.Fprintf(r.out, "  %s\n", d)
		}
		return
	}
	fmt.Fprintf(r.out, "%s %s %s\n",
		r.pal.Info(r.sym.Run+" "+idx(i, n)),
		r.pal.Bold(path),
		r.pal.Dim("("+size+")"))
	for _, d := range details {
		fmt.Fprintln(r.out, r.pal.Dim("  "+d))
	}
}

// Detail prints an indented sub-line (plan entries, track lists).
func (r *Reporter) Detail(format string, args ...any) {
	r.close()
	msg := fmt.Sprintf(format, args...)
	if r.p.Interactive {
		fmt.Fprintln(r.out, r.pal.Dim("  "+msg))
		return
	}
	fmt.Fprintf(r.out, "  %s\n", msg)
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

// status rewrites the live line in place.
func (r *Reporter) status(s string) {
	fmt.Fprintf(r.out, "\r\x1b[K%s", s)
	r.live = true
}

// Success closes the current file: "<verb>: <path> (<in> -> <out>,
// backup: <path>.old)" plain, a green result line replacing the bar
// pretty. took is the per-file duration.
func (r *Reporter) Success(verb, path string, inBytes, outBytes int64, backup bool, took time.Duration) {
	if !r.p.Interactive {
		if backup {
			fmt.Fprintf(r.out, "%s: %s (%s -> %s, backup: %s.old)\n", verb, path, media.HumanBytes(inBytes), media.HumanBytes(outBytes), path)
			return
		}
		fmt.Fprintf(r.out, "%s: %s (original kept)\n", verb, path)
		return
	}
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
		fmt.Fprintf(r.out, "Error: %s\n", msg)
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
		fmt.Fprintln(os.Stderr, msg)
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
		fmt.Fprintf(r.out, "  encoding with %s (attempt %d/%d: %s)...\n", enc, i, n, label)
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
		fmt.Fprintf(r.out, "  attempt %d (%s) failed...\n", i, label)
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
	if !r.p.Interactive {
		fmt.Fprint(r.out, msg)
		return
	}
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
		fmt.Fprintln(r.out, strings.Repeat("=", 42))
		if skipLabel == "" {
			fmt.Fprintf(r.out, "%s %s: %d.\n", lead, doneLabel, o.Done)
			return
		}
		fmt.Fprintf(r.out, "%s %s: %d, %s: %d, failed: %d.\n", lead, doneLabel, o.Done, skipLabel, o.Skipped, o.Failed)
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
