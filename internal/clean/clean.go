// Package clean removes audio and subtitle tracks whose language is outside
// the whitelist (default: Czech + English) with a pure -c copy remux.
// Port of the bash clean-tracks.sh: sole-track protection, cover-art
// removal, safety gates, .old backups and dry runs.
package clean

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/backup"
	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/gates"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/walk"
)

// Config holds the clean subcommand settings.
type Config struct {
	Target    string
	Langs     string
	PurgeSole bool
	RemoveOld bool
	Ignores   []string
	DryRun    bool
	NoBackup  bool
	Verbose   bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	whitelist  []string
	ignores    ignore.List
	rep        *ui.Reporter
	guard      *run.TmpGuard

	cleaned, untouched, failed int
	bytesIn, bytesOut          int64
	failures                   []ui.Failure
}

// fail renders the standard "Error: ..." line and records the failure for
// the summary.
func (t *tool) fail(path, msg string) {
	t.rep.Fail(msg, "")
	t.failures = append(t.failures, ui.Failure{Path: path, Msg: msg})
	t.failed++
}

// swap puts the new file in place, honoring -no-backup. Returns the backup
// path (empty when the original was removed instead).
func (t *tool) swap(path, tmp string) (string, error) {
	if t.cfg.NoBackup {
		return "", backup.Replace(path, tmp)
	}
	return backup.Swap(path, tmp)
}

// Run executes the clean subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("clean", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.StringVar(&cfg.Langs, "l", "cs,en", "whitelist languages, comma separated (cs/en expand to their alias spellings)")
	fs.BoolVar(&cfg.PurgeSole, "e", false, "purge even the only track of its type when its language is not whitelisted")
	fs.BoolVar(&cfg.RemoveOld, "old", false, "remove the .old backup files of previous runs instead of cleaning tracks (-l/-e do not apply)")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be removed")
	fs.BoolVar(&cfg.NoBackup, "no-backup", false, "replace files in place without keeping .old backups (the originals are removed)")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool clean [flags] <directory|file>")
		return fmt.Errorf("clean needs exactly one directory or file argument")
	}
	var target string
	var err error
	if cfg.RemoveOld {
		// the -old mode deletes backups, so the target may be a .old file
		// (no media extension) as well as a directory
		target, err = cli.ResolvePath(fs.Arg(0))
	} else {
		target, err = cli.ResolveMediaTarget(fs.Arg(0), true)
	}
	if err != nil {
		return err
	}
	cfg.Target = target

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	ignores, warnings := ignore.Load(cli.IgnoresDir(target), "clean", cfg.Ignores)
	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)
	for _, w := range warnings {
		rep.Raw(ui.WarnTone, "warning: %s\n", w)
	}

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		whitelist:  media.ExpandWhitelist(strings.Split(cfg.Langs, ",")),
		ignores:    ignores,
		rep:        rep,
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
	if t.cfg.RemoveOld {
		return t.runOld()
	}
	soleNote := "kept even when not whitelisted"
	if t.cfg.PurgeSole {
		soleNote = "purged too (-e)"
	}
	backupNote := "keep .old"
	if t.cfg.NoBackup {
		backupNote = "none (-no-backup)"
	}
	kvs := []ui.KV{
		ui.Pair("FFmpeg binary", t.ffmpegBin),
		ui.Pair("Keep languages", strings.Join(t.whitelist, " ")+" (+ no/und tags)"),
		ui.Pair("Sole tracks", soleNote),
		ui.Pair("Backups", backupNote),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
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
		t.rep.Found(len(files))
		for i, path := range files {
			if t.ignores.Match(path) {
				t.rep.Skip(i+1, len(files), path, "ignored")
				continue
			}
			t.processFile(i+1, len(files), path)
		}
	} else if t.ignores.Match(target) {
		t.rep.Skip(1, 1, target, "ignored")
	} else {
		t.processFile(1, 1, target)
	}

	t.rep.Summary("Done.", "Cleaned", "untouched", ui.Outcome{
		Done:     t.cleaned,
		Skipped:  t.untouched,
		Failed:   t.failed,
		BytesIn:  t.bytesIn,
		BytesOut: t.bytesOut,
		Failures: t.failures,
		Elapsed:  time.Since(start),
	})
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

func (t *tool) skip(i, n int, path, reason string) {
	t.rep.Skip(i, n, path, reason)
	t.untouched++
}

// runOld removes the .old backup files of previous runs (-old mode): a
// walk for .old files, honoring the ignore rules, with the usual dry run.
func (t *tool) runOld() error {
	kvs := []ui.KV{
		ui.Pair("Mode", "remove .old backup files"),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
	target := t.cfg.Target
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	var files []string
	if info.IsDir() {
		if files, err = walk.OldFiles(target); err != nil {
			return err
		}
	} else {
		// single-file target: remove just that one backup
		files = []string{target}
	}
	t.rep.Found(len(files))
	for i, path := range files {
		if t.ignores.Match(path) {
			t.rep.Skip(i+1, len(files), path, "ignored")
			t.untouched++
			continue
		}
		t.removeOld(i+1, len(files), path)
	}

	t.rep.Summary("Done.", "Removed", "kept", ui.Outcome{
		Done:     t.cleaned,
		Skipped:  t.untouched,
		Failed:   t.failed,
		BytesIn:  t.bytesIn,
		Failures: t.failures,
		Elapsed:  time.Since(start),
	})
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

// removeOld deletes one .old backup file. A single-file target that is not
// a .old file is refused - only the tool's own backups are removable here.
func (t *tool) removeOld(i, n int, path string) {
	if !strings.HasSuffix(filepath.Base(path), ".old") {
		t.rep.Skip(i, n, path, "not a .old file")
		t.untouched++
		return
	}
	st, err := os.Stat(path)
	if err != nil {
		t.fail(path, err.Error())
		return
	}
	size := media.HumanBytes(st.Size())
	if t.cfg.DryRun {
		t.rep.Would("Would remove", i, n, path, size)
		t.cleaned++
		t.bytesIn += st.Size()
		return
	}
	if err := os.Remove(path); err != nil {
		t.fail(path, err.Error())
		return
	}
	t.rep.Removed(i, n, path, size)
	t.cleaned++
	t.bytesIn += st.Size()
}

// TrackPlan is the audio/subtitle removal plan for one file under a
// language whitelist: the input per-type ordinals to drop and the counts
// that must survive the remux. Sole tracks are kept unless purgeSole.
// Lines describes the removals; SoleKept lists the foreign sole tracks
// kept by the sole-track rule, which callers surface as a notice.
type TrackPlan struct {
	RemoveAudio []int
	RemoveSubs  []int
	KeptAudio   int
	KeptSubs    int
	Lines       []string
	SoleKept    []string
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
			plan.SoleKept = append(plan.SoleKept, fmt.Sprintf("keep audio #%d (%s, lang '%s') - sole audio track", ta.Ordinal, ta.Stream.CodecName, media.LangDisplay(lang)))
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
			plan.SoleKept = append(plan.SoleKept, fmt.Sprintf("keep subtitle #%d (%s, lang '%s') - sole subtitle track", ts.Ordinal, ts.Stream.CodecName, media.LangDisplay(lang)))
			continue
		}
		plan.RemoveSubs = append(plan.RemoveSubs, ts.Ordinal)
		plan.Lines = append(plan.Lines, fmt.Sprintf("remove subtitle #%d (%s, lang '%s')", ts.Ordinal, ts.Stream.CodecName, media.LangDisplay(lang)))
		plan.KeptSubs--
	}
	return plan
}

// broadcastContainer reports whether path is a broadcast/dump container
// clean cannot remux losslessly: it only maps video/audio/subtitles (so
// data streams like teletext are dropped) and would rewrite the container
// under the original name. convert handles these formats instead.
func broadcastContainer(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ts", ".mts", ".m2ts", ".mpg", ".mpeg":
		return true
	}
	return false
}

func (t *tool) processFile(i, n int, path string) {
	if broadcastContainer(path) {
		t.skip(i, n, path, "broadcast container; clean only handles mkv/mp4")
		return
	}
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		t.skip(i, n, path, fmt.Sprintf("cannot probe: %v", err))
		return
	}

	src := countStreams(info)
	if src.video == 0 {
		t.skip(i, n, path, "no real video stream")
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

	// foreign sole tracks survive the sole-track rule; surface that as a
	// notice even when nothing else is removed (the file stays untouched)
	for _, s := range trackPlan.SoleKept {
		t.rep.Notice(i, n, path, s)
	}

	if len(removeArgs) == 0 {
		t.rep.OK(i, n, path, "nothing to remove")
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
		t.rep.Would("Would clean", i, n, path, media.HumanBytes(srcSize), plan...)
		t.cleaned++
		return
	}

	srcDur := time.Duration(0)
	if d, ok := info.Duration(); ok {
		srcDur = time.Duration(d * float64(time.Second))
	}
	fileStart := time.Now()
	t.rep.Start("Cleaning", i, n, path, media.HumanBytes(srcSize), srcDur, plan...)

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
	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !t.cfg.Verbose {
		onStats = t.rep.Progress
	}
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, args...); err != nil {
		t.fail(path, fmt.Sprintf("failed to clean %s (left untouched)", path))
		return
	}

	if !t.gatesPass(path, tmp, info, srcSize, keep) {
		return
	}

	newSize := int64(0)
	if st, err := os.Stat(tmp); err == nil {
		newSize = st.Size()
	}
	backupPath, err := t.swap(path, tmp)
	if err != nil {
		t.fail(path, err.Error())
		return
	}
	if t.cfg.NoBackup {
		t.rep.SuccessNoBackup("Cleaned", path, srcSize, newSize, time.Since(fileStart))
	} else {
		t.rep.Success("Cleaned", path, srcSize, newSize, backupPath, time.Since(fileStart))
	}
	t.bytesIn += srcSize
	t.bytesOut += newSize
	t.cleaned++
}

// gatesPass verifies duration, size and the exact planned stream counts of
// the remuxed output; on failure it reports and leaves the original.
func (t *tool) gatesPass(path, tmp string, info *probe.Info, srcSize int64, keep counts) bool {
	newInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		t.fail(path, fmt.Sprintf("cannot probe the output: %v", err))
		return false
	}
	if srcDur, ok1 := info.Duration(); ok1 {
		if newDur, ok2 := newInfo.Duration(); ok2 && !gates.DurationKept(newDur, srcDur, 0.99) {
			t.fail(path, fmt.Sprintf("output (%.3fs) is much shorter than source (%.3fs) - keeping original", newDur, srcDur))
			return false
		}
	}
	st, err := os.Stat(tmp)
	if err != nil {
		t.fail(path, fmt.Sprintf("output is missing: %v", err))
		return false
	}
	if !gates.Shrunk(st.Size(), srcSize) {
		t.fail(path, fmt.Sprintf("output is not smaller (%s -> %s) - keeping original", media.HumanBytes(srcSize), media.HumanBytes(st.Size())))
		return false
	}
	newCounts := countStreams(newInfo)
	if newCounts != keep {
		t.fail(path, fmt.Sprintf("output streams (video %d, audio %d, subs %d) do not match the plan (video %d, audio %d, subs %d) - keeping original",
			newCounts.video, newCounts.audio, newCounts.subtitle, keep.video, keep.audio, keep.subtitle))
		return false
	}
	return true
}
