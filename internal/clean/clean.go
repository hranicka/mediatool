// Package clean removes audio and subtitle tracks whose language is outside
// the whitelist (default: Czech + English) with a pure -c copy remux.
// Port of the bash clean-tracks.sh: sole-track protection, cover-art
// removal, safety gates, .old backups and dry runs.
package clean

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/hranicka/media-tool/internal/backup"
	"github.com/hranicka/media-tool/internal/gates"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/media"
	"github.com/hranicka/media-tool/internal/probe"
	"github.com/hranicka/media-tool/internal/run"
	"github.com/hranicka/media-tool/internal/walk"
)

// Config holds the clean subcommand settings.
type Config struct {
	Target    string
	Langs     string
	PurgeSole bool
	Ignores   []string
	DryRun    bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	whitelist  []string
	ignores    ignore.List
	out        io.Writer
	guard      *run.TmpGuard

	cleaned, untouched, failed int
}

// Run executes the clean subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.StringVar(&cfg.Langs, "l", "cs,en", "whitelist languages, comma separated (cs/en expand to their alias spellings)")
	fs.BoolVar(&cfg.PurgeSole, "e", false, "purge even the only track of its type when its language is not whitelisted")
	fs.Var(cliIgnores{&cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be removed")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: media-tool clean [flags] <directory|file>")
		return fmt.Errorf("clean needs exactly one directory or file argument")
	}
	target, err := resolveTarget(fs.Arg(0))
	if err != nil {
		return err
	}
	cfg.Target = target

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		whitelist:  media.ExpandWhitelist(strings.Split(cfg.Langs, ",")),
		ignores:    ignore.Load(ignoresDir(target), "clean", cfg.Ignores),
		out:        os.Stdout,
		guard:      run.NewTmpGuard(),
	}
	return t.runAll()
}

func resolveTarget(arg string) (string, error) {
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
		switch strings.ToLower(filepath.Ext(target)) {
		case ".mkv", ".mp4":
		default:
			return "", fmt.Errorf("%s is neither a media file nor a directory", target)
		}
	}
	return target, nil
}

// ignoresDir returns the directory whose dot ignore files govern the target:
// the target's own directory for a single file.
func ignoresDir(target string) string {
	if info, err := os.Stat(target); err == nil && !info.IsDir() {
		return filepath.Dir(target)
	}
	return target
}

// cliIgnores adapts a comma-separated flag value into a string slice.
type cliIgnores struct{ dst *[]string }

func (v cliIgnores) String() string { return strings.Join(*v.dst, ",") }

func (v cliIgnores) Set(s string) error {
	for _, e := range strings.Split(s, ",") {
		if e != "" {
			*v.dst = append(*v.dst, e)
		}
	}
	return nil
}

// counts tallies real video (non-cover-art), audio and subtitle streams.
type counts struct {
	video, audio, subtitle int
}

func countStreams(info *probe.Info) counts {
	var c counts
	for _, s := range info.Streams {
		switch s.CodecType {
		case probe.TypeVideo:
			if !media.IsCoverArt(s) {
				c.video++
			}
		case probe.TypeAudio:
			c.audio++
		case probe.TypeSubtitle:
			c.subtitle++
		}
	}
	return c
}

func (t *tool) runAll() error {
	soleNote := "kept even when not whitelisted"
	if t.cfg.PurgeSole {
		soleNote = "purged too (-e)"
	}
	fmt.Fprintf(t.out, "FFmpeg binary : %s\n", t.ffmpegBin)
	fmt.Fprintf(t.out, "Keep languages: %s (+ no/und tags)\n", strings.Join(t.whitelist, " "))
	fmt.Fprintf(t.out, "Sole tracks   : %s\n", soleNote)
	fmt.Fprintf(t.out, "Ignore entries: %d\n", t.ignores.Len())
	if t.cfg.DryRun {
		fmt.Fprintf(t.out, "Extra         : DRY RUN\n")
	}
	fmt.Fprintln(t.out, "==========================================")

	target := t.cfg.Target
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() {
		files, err := walk.Media(target)
		if err != nil {
			return err
		}
		for _, path := range files {
			if t.ignores.Match(path) {
				fmt.Fprintf(t.out, "Skipping (ignored): %s\n", path)
				continue
			}
			t.processFile(path)
		}
	} else if t.ignores.Match(target) {
		fmt.Fprintf(t.out, "Skipping (ignored): %s\n", target)
	} else {
		t.processFile(target)
	}

	fmt.Fprintln(t.out, "==========================================")
	fmt.Fprintf(t.out, "Done. Cleaned: %d, untouched: %d, failed: %d.\n", t.cleaned, t.untouched, t.failed)
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

func (t *tool) skip(format string, args ...any) {
	fmt.Fprintf(t.out, "Skipping ("+format+"): %s\n", args...)
	t.untouched++
}

func (t *tool) processFile(path string) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		fmt.Fprintf(t.out, "Skipping (cannot probe: %v): %s\n", err, path)
		t.untouched++
		return
	}

	src := countStreams(info)
	if src.video == 0 {
		t.skip("no real video stream", path)
		return
	}

	// decide removals; the sole-track rule counts the totals first
	var removeArgs []string
	var plan []string
	keep := src
	for _, tv := range info.ByType()[probe.TypeVideo] {
		if media.IsCoverArt(tv.Stream) {
			removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:v:%d", tv.Ordinal))
			plan = append(plan, fmt.Sprintf("remove cover art video #%d (%s)", tv.Ordinal, tv.Stream.CodecName))
		}
	}
	for _, ta := range info.ByType()[probe.TypeAudio] {
		lang := ta.Stream.Tags.Language
		if media.LangKept(lang, t.whitelist) {
			continue
		}
		if src.audio == 1 && !t.cfg.PurgeSole {
			plan = append(plan, fmt.Sprintf("keep audio #%d (%s, lang '%s') - sole audio track", ta.Ordinal, ta.Stream.CodecName, lang))
			continue
		}
		removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:a:%d", ta.Ordinal))
		plan = append(plan, fmt.Sprintf("remove audio #%d (%s, lang '%s')", ta.Ordinal, ta.Stream.CodecName, lang))
		keep.audio--
	}
	for _, ts := range info.ByType()[probe.TypeSubtitle] {
		lang := ts.Stream.Tags.Language
		if media.LangKept(lang, t.whitelist) {
			continue
		}
		if src.subtitle == 1 && !t.cfg.PurgeSole {
			plan = append(plan, fmt.Sprintf("keep subtitle #%d (%s, lang '%s') - sole subtitle track", ts.Ordinal, ts.Stream.CodecName, lang))
			continue
		}
		removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:s:%d", ts.Ordinal))
		plan = append(plan, fmt.Sprintf("remove subtitle #%d (%s, lang '%s')", ts.Ordinal, ts.Stream.CodecName, lang))
		keep.subtitle--
	}

	if len(removeArgs) == 0 {
		fmt.Fprintf(t.out, "OK (nothing to remove): %s\n", path)
		t.untouched++
		return
	}

	srcSize := int64(0)
	if s, ok := info.Size(); ok {
		srcSize = s
	}
	if st, err := os.Stat(path); err == nil {
		srcSize = st.Size()
	}

	if t.cfg.DryRun {
		fmt.Fprintf(t.out, "Would clean: %s (%s)\n", path, media.HumanBytes(srcSize))
		for _, line := range plan {
			fmt.Fprintf(t.out, "  - %s\n", line)
		}
		t.cleaned++
		return
	}

	fmt.Fprintf(t.out, "Cleaning: %s (%s)\n", path, media.HumanBytes(srcSize))
	for _, line := range plan {
		fmt.Fprintf(t.out, "  - %s\n", line)
	}

	var fmtName string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		fmtName = "mp4"
	default:
		fmtName = "matroska"
	}

	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	t.guard.Add(tmp)
	defer func() {
		t.guard.Remove(tmp)
		if _, err := os.Stat(tmp); err == nil {
			os.Remove(tmp)
		}
	}()

	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y", "-i", path,
		"-map", "0:v", "-map", "0:a?", "-map", "0:s?"}
	args = append(args, removeArgs...)
	args = append(args, "-c", "copy", "-map_metadata:g", "0:g", "-max_muxing_queue_size", "4096", "-f", fmtName, tmp)
	if _, err := t.r.Stream(t.ffmpegBin, args...); err != nil {
		fmt.Fprintf(t.out, "Error: failed to clean %s (left untouched)\n", path)
		t.failed++
		return
	}

	if !t.gatesPass(path, tmp, info, srcSize, keep) {
		return
	}

	newSize := int64(0)
	if st, err := os.Stat(tmp); err == nil {
		newSize = st.Size()
	}
	if _, err := backup.Swap(path, tmp); err != nil {
		fmt.Fprintf(t.out, "Error: %v\n", err)
		t.failed++
		return
	}
	fmt.Fprintf(t.out, "Cleaned: %s (%s -> %s, backup: %s.old)\n", path, media.HumanBytes(srcSize), media.HumanBytes(newSize), path)
	t.cleaned++
}

// gatesPass verifies duration, size and the exact planned stream counts of
// the remuxed output; on failure it reports and leaves the original.
func (t *tool) gatesPass(path, tmp string, info *probe.Info, srcSize int64, keep counts) bool {
	newInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		fmt.Fprintf(t.out, "Error: cannot probe the output: %v\n", err)
		t.failed++
		return false
	}
	if srcDur, ok1 := info.Duration(); ok1 {
		if newDur, ok2 := newInfo.Duration(); ok2 && !gates.DurationKept(newDur, srcDur, 0.99) {
			fmt.Fprintf(t.out, "Error: output (%.3fs) is much shorter than source (%.3fs) - keeping original\n", newDur, srcDur)
			t.failed++
			return false
		}
	}
	st, err := os.Stat(tmp)
	if err != nil {
		fmt.Fprintf(t.out, "Error: output is missing: %v\n", err)
		t.failed++
		return false
	}
	if !gates.Shrunk(st.Size(), srcSize) {
		fmt.Fprintf(t.out, "Error: output is not smaller (%s -> %s) - keeping original\n", media.HumanBytes(srcSize), media.HumanBytes(st.Size()))
		t.failed++
		return false
	}
	newCounts := countStreams(newInfo)
	if newCounts != keep {
		fmt.Fprintf(t.out, "Error: output streams (video %d, audio %d, subs %d) do not match the plan (video %d, audio %d, subs %d) - keeping original\n",
			newCounts.video, newCounts.audio, newCounts.subtitle, keep.video, keep.audio, keep.subtitle)
		t.failed++
		return false
	}
	return true
}
