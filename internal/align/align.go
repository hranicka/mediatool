// Package align appends a dubbed audio track to a reference video by
// scene matching: frames sampled from both files are perceptually hashed
// (shared with dedup via vhash), each dubbed frame is matched against a
// windowed neighborhood of the reference, and a smooth monotonic timing
// map (quadratic fit with outlier rejection) drives the resampling of the
// dub onto the reference timeline. The output muxes the reference video,
// its own audio and subtitles plus the resampled dub as a second track.
// Go port of align_audio.py.
package align

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

// Config holds the align subcommand settings.
type Config struct {
	Ref, Dub  string
	Out       string
	Interval  float64
	MatchDist int
	Bitrate   string
	LangRef   string
	LangDub   string
	DryRun    bool
	Verbose   bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	rep        *ui.Reporter
	guard      *run.TmpGuard
}

// Run executes the align subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("align", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.Float64Var(&cfg.Interval, "interval", 0.2, "frame sampling interval in seconds (smaller = finer map, slower)")
	fs.IntVar(&cfg.MatchDist, "match-dist", 45, "max hash distance (of 1024 bits) for a reliable frame match")
	fs.StringVar(&cfg.Bitrate, "bitrate", "192k", "bitrate of the resampled dub track (AAC)")
	fs.StringVar(&cfg.LangRef, "lang-ref", "eng", "language tag of the reference audio track")
	fs.StringVar(&cfg.LangDub, "lang-dub", "ces", "language tag of the dubbed audio track")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: stop after building the timing map and print it")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if fs.NArg() < 2 || fs.NArg() > 3 {
		fmt.Fprintln(os.Stderr, "usage: mediatool align [flags] <reference> <dubbed> [output]")
		return fmt.Errorf("align needs a reference and a dubbed file (and an optional output path)")
	}
	var err error
	if cfg.Ref, err = cli.ResolvePath(fs.Arg(0)); err != nil {
		return err
	}
	if cfg.Dub, err = cli.ResolvePath(fs.Arg(1)); err != nil {
		return err
	}
	cfg.Out = fs.Arg(2)
	if cfg.Out == "" {
		cfg.Out = strings.TrimSuffix(cfg.Ref, filepath.Ext(cfg.Ref)) + "+dub.mkv"
	}
	if _, err := os.Stat(cfg.Out); err == nil {
		return fmt.Errorf("output %s already exists", cfg.Out)
	}

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		rep:        rep,
		guard:      run.NewTmpGuard(),
	}
	defer t.guard.Cleanup()
	return t.runAll()
}

func (t *tool) runAll() error {
	refInfo, dubInfo, err := t.probeInputs()
	if err != nil {
		return err
	}
	refDur, _ := refInfo.Duration()
	dubDur, _ := dubInfo.Duration()

	kvs := []ui.KV{
		ui.Pair("Reference", fmt.Sprintf("%s (%s)", t.cfg.Ref, clock(refDur))),
		ui.Pair("Dubbed", fmt.Sprintf("%s (%s)", t.cfg.Dub, clock(dubDur))),
		ui.Pair("Output", t.cfg.Out),
		ui.Pair("Frame interval", fmt.Sprintf("every %gs", t.cfg.Interval)),
		ui.Pair("Match distance", fmt.Sprintf("%d of 1024 bits", t.cfg.MatchDist)),
		ui.Pair("Dub track", fmt.Sprintf("aac %s, tagged %s", t.cfg.Bitrate, t.cfg.LangDub)),
		ui.Pair("Ref audio", fmt.Sprintf("copied, tagged %s", t.cfg.LangRef)),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()

	// frames: one full decode per file
	t.rep.Phase("extracting reference frames (decodes the whole file)...")
	refHashes, err := vhash.Extract(t.r, t.ffmpegBin, t.cfg.Ref, t.cfg.Interval)
	if err != nil {
		return fmt.Errorf("reference: %w", err)
	}
	t.rep.Phase("extracting dubbed frames (decodes the whole file)...")
	dubHashes, err := vhash.Extract(t.r, t.ffmpegBin, t.cfg.Dub, t.cfg.Interval)
	if err != nil {
		return fmt.Errorf("dubbed: %w", err)
	}
	slog.Debug("frames", "ref", len(refHashes), "dub", len(dubHashes))
	if len(refHashes) == 0 || len(dubHashes) == 0 {
		return fmt.Errorf("no frames extracted (reference %d, dubbed %d)", len(refHashes), len(dubHashes))
	}

	// self-similarity guard: on synthetic or static content a match is
	// barely better evidence than chance, however small its distance
	minFrames := max(1, int(5.0/t.cfg.Interval))
	bgRef := vhash.BackgroundDistance(refHashes, minFrames, 200)
	bgDub := vhash.BackgroundDistance(dubHashes, minFrames, 200)
	bg := minPositive(bgRef, bgDub)
	if bg >= 0 && bg < vhash.SelfSimilarBits {
		t.rep.Note(fmt.Sprintf("note: frames are highly self-similar (background median %d of %d bits) - scene matches carry little evidence, verify the timing map carefully", bg, vhash.Side*vhash.Side))
	}

	// matching + the timing map
	window := max(50, int(5.0/t.cfg.Interval))
	matches := findMatches(refHashes, dubHashes, window)
	czGrid, enGrid, nRel, nOut, err := buildTimingMap(matches, t.cfg.Interval, dubDur, t.cfg.MatchDist)
	if err != nil {
		return err
	}
	if err := plausibleMap(enGrid, dubDur); err != nil {
		return err
	}
	t.rep.Raw(ui.GoodTone, "Timing map: %d reliable matches, %d outliers dropped\n", nRel, nOut)
	t.rep.Raw(ui.Neutral, "dub 0s -> ref %.1fs; dub %.0fs -> ref %.1fs\n", enGrid[0], dubDur, enGrid[len(enGrid)-1])
	step := max(1, int(dubDur)/10)
	for s := 0; s < int(dubDur); s += step {
		t.rep.Raw(ui.Neutral, "  dub %5ds -> ref %6.1fs  (offset %+.1fs)\n", s, enGrid[s], enGrid[s]-float64(s))
	}

	if t.cfg.DryRun {
		t.rep.Summary("Done.", "Timing map built", "", ui.Outcome{
			Done:    1,
			Elapsed: time.Since(start),
		})
		return nil
	}

	if err := t.mux(refInfo, czGrid, enGrid, refDur, dubDur); err != nil {
		return err
	}

	newSize := int64(0)
	if st, statErr := os.Stat(t.cfg.Out); statErr == nil {
		newSize = st.Size()
	}
	t.rep.Success("Created", t.cfg.Out, 0, newSize, "", time.Since(start))
	t.rep.Summary("Done.", "Aligned", "", ui.Outcome{
		Done:    1,
		Elapsed: time.Since(start),
	})
	return nil
}

// probeInputs checks both files and that the dub carries an audio track.
func (t *tool) probeInputs() (refInfo, dubInfo *probe.Info, err error) {
	if refInfo, err = probe.Probe(t.r, t.ffprobeBin, t.cfg.Ref); err != nil {
		return nil, nil, fmt.Errorf("reference: %w", err)
	}
	if dubInfo, err = probe.Probe(t.r, t.ffprobeBin, t.cfg.Dub); err != nil {
		return nil, nil, fmt.Errorf("dubbed: %w", err)
	}
	if len(dubInfo.ByType()[probe.TypeAudio]) == 0 {
		return nil, nil, fmt.Errorf("%s has no audio track to align", t.cfg.Dub)
	}
	return refInfo, dubInfo, nil
}

// mux extracts the dub audio, resamples it onto the reference timeline
// and muxes reference video + audio + subs with the dub appended.
func (t *tool) mux(refInfo *probe.Info, czGrid, enGrid []float64, refDur, dubDur float64) error {
	work, err := os.MkdirTemp("", "mediatool-align-")
	if err != nil {
		return err
	}
	t.guard.Add(work)
	dubRaw := filepath.Join(work, "dub.raw")
	resRaw := filepath.Join(work, "dub_resampled.raw")

	// the dub's audio as raw s16le stereo
	t.rep.Phase("extracting the dubbed audio...")
	if _, err := t.r.Stream(t.ffmpegBin, "-v", "error", "-y", "-i", t.cfg.Dub,
		"-map", "0:a:0", "-ac", "2", "-ar", fmt.Sprint(sampleRate), "-f", "s16le", dubRaw); err != nil {
		return fmt.Errorf("cannot extract the dubbed audio: %w", err)
	}

	// resample onto the reference timeline
	t.rep.Phase("resampling the dub onto the reference timeline...")
	src, err := os.Open(dubRaw)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(resRaw)
	if err != nil {
		return err
	}
	if err := resampleDub(src, dst, czGrid, enGrid, refDur); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}

	// mux: reference video + reference audio + subs + resampled dub
	nSub := len(refInfo.ByType()[probe.TypeSubtitle])
	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y",
		"-i", t.cfg.Ref,
		"-f", "s16le", "-ar", fmt.Sprint(sampleRate), "-ac", "2", "-i", resRaw,
		"-map", "0:v:0", "-map", "0:a:0"}
	for s := 0; s < nSub; s++ {
		args = append(args, "-map", fmt.Sprintf("0:s:%d", s))
	}
	args = append(args, "-map", "1:a:0",
		"-c:v", "copy", "-c:a:0", "copy", "-c:s", "copy",
		"-c:a:1", "aac", "-b:a:1", t.cfg.Bitrate,
		"-metadata:s:a:0", "language="+t.cfg.LangRef, "-metadata:s:a:0", "title=Reference",
		"-metadata:s:a:1", "language="+t.cfg.LangDub, "-metadata:s:a:1", "title=Dubbed (resampled)",
		t.cfg.Out)

	t.rep.Start("Aligning", 1, 1, t.cfg.Out, "", time.Duration(refDur*float64(time.Second)))
	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !t.cfg.Verbose {
		onStats = t.rep.Progress
	}
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, args...); err != nil {
		return fmt.Errorf("muxing failed: %w", err)
	}
	return nil
}

// clock renders a media duration as h:mm:ss.
func clock(d float64) string {
	sec := int(d)
	return fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
}

// minPositive returns the smallest non-negative value, or -1 when all are
// negative (BackgroundDistance's "not enough frames" marker).
func minPositive(values ...int) int {
	bg := -1
	for _, v := range values {
		if v >= 0 && (bg == -1 || v < bg) {
			bg = v
		}
	}
	return bg
}
