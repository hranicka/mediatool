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

	"github.com/hranicka/mediatool/internal/backup"
	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/gates"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/walk"
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
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be removed")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool clean [flags] <directory|file>")
		return fmt.Errorf("clean needs exactly one directory or file argument")
	}
	target, err := cli.ResolveMediaTarget(fs.Arg(0), true)
	if err != nil {
		return err
	}
	cfg.Target = target

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	ignores, warnings := ignore.Load(cli.IgnoresDir(target), "clean", cfg.Ignores)
	for _, w := range warnings {
		fmt.Fprintln(os.Stdout, "warning:", w)
	}

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		whitelist:  media.ExpandWhitelist(strings.Split(cfg.Langs, ",")),
		ignores:    ignores,
		out:        os.Stdout,
		guard:      run.NewTmpGuard(),
	}
	return t.runAll()
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

// TrackPlan is the audio/subtitle removal plan for one file under a
// language whitelist: the input per-type ordinals to drop and the counts
// that must survive the remux. Sole tracks are kept unless purgeSole.
type TrackPlan struct {
	RemoveAudio []int
	RemoveSubs  []int
	KeptAudio   int
	KeptSubs    int
	Lines       []string
}

// PlanTracks classifies audio and subtitle tracks against the whitelist —
// the clean command's rules, shared with convert's -l cleanup: a track
// whose language is outside the whitelist is removed, unless it is the
// only track of its type and purgeSole is false.
func PlanTracks(info *probe.Info, whitelist []string, purgeSole bool) TrackPlan {
	src := countStreams(info)
	plan := TrackPlan{KeptAudio: src.audio, KeptSubs: src.subtitle}
	for _, ta := range info.ByType()[probe.TypeAudio] {
		lang := ta.Stream.Tags.Language
		if media.LangKept(lang, whitelist) {
			continue
		}
		if src.audio == 1 && !purgeSole {
			plan.Lines = append(plan.Lines, fmt.Sprintf("keep audio #%d (%s, lang '%s') - sole audio track", ta.Ordinal, ta.Stream.CodecName, media.LangDisplay(lang)))
			continue
		}
		plan.RemoveAudio = append(plan.RemoveAudio, ta.Ordinal)
		plan.Lines = append(plan.Lines, fmt.Sprintf("remove audio #%d (%s, lang '%s')", ta.Ordinal, ta.Stream.CodecName, media.LangDisplay(lang)))
		plan.KeptAudio--
	}
	for _, ts := range info.ByType()[probe.TypeSubtitle] {
		lang := ts.Stream.Tags.Language
		if media.LangKept(lang, whitelist) {
			continue
		}
		if src.subtitle == 1 && !purgeSole {
			plan.Lines = append(plan.Lines, fmt.Sprintf("keep subtitle #%d (%s, lang '%s') - sole subtitle track", ts.Ordinal, ts.Stream.CodecName, media.LangDisplay(lang)))
			continue
		}
		plan.RemoveSubs = append(plan.RemoveSubs, ts.Ordinal)
		plan.Lines = append(plan.Lines, fmt.Sprintf("remove subtitle #%d (%s, lang '%s')", ts.Ordinal, ts.Stream.CodecName, media.LangDisplay(lang)))
		plan.KeptSubs--
	}
	return plan
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

	// decide removals: cover art plus the whitelisted-track plan
	trackPlan := PlanTracks(info, t.whitelist, t.cfg.PurgeSole)
	var removeArgs []string
	var plan []string
	for _, tv := range info.ByType()[probe.TypeVideo] {
		if media.IsCoverArt(tv.Stream) {
			removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:v:%d", tv.Ordinal))
			plan = append(plan, fmt.Sprintf("remove cover art video #%d (%s)", tv.Ordinal, tv.Stream.CodecName))
		}
	}
	for _, o := range trackPlan.RemoveAudio {
		removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:a:%d", o))
	}
	for _, o := range trackPlan.RemoveSubs {
		removeArgs = append(removeArgs, "-map", fmt.Sprintf("-0:s:%d", o))
	}
	plan = append(plan, trackPlan.Lines...)
	keep := counts{video: src.video, audio: trackPlan.KeptAudio, subtitle: trackPlan.KeptSubs}

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
