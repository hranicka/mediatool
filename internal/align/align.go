// Package align appends a dubbed audio track to a reference video by
// timing the dub onto the reference timeline: frames sampled from both
// files are perceptually hashed (shared with dedup via vhash) and a
// timing map is fitted robustly — dub masters often run at a different
// speed (PAL speedup, NTSC transfers), may start or end earlier or later
// than the reference (a modified intro or credits), and TV broadcasts
// often carry a few trimmed scenes mid-file, so the map is piecewise with
// one shared speed and per-segment offsets. A single pair can be aligned
// ad hoc, or a whole directory can be paired by episode/title naming
// (weak episode-tag matches verified by video content) and processed in
// place with .old backups.
package align

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/vhash"
)

// Config holds the align subcommand settings.
type Config struct {
	Ref, Dub       string
	RefDir, DubDir string
	Mixed          bool
	Out            string
	DubSuffix      string
	Pairs          []string
	PairOnly       bool
	NoContentPair  bool
	NoBackup       bool
	Ignores        []string
	Interval       float64
	MatchDist      int
	Bitrate        string
	LangRef        string
	LangDub        string
	CacheDir       string
	NoCache        bool
	CleanCache     bool
	DryRun         bool
	Verbose        bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	cache      vhash.Cache
	rep        *ui.Reporter
	guard      *run.TmpGuard

	aligned, skipped, failed int
	bytesIn, bytesOut        int64
	failures                 []ui.Failure
}

// inputs is the per-pair metadata shared by the single-pair and batch
// pipelines.
type inputs struct {
	ref, dub         string
	refInfo, dubInfo *probe.Info
	refDur, dubDur   float64
	dubAudio         probe.Typed
	dubLangMatched   bool
	dubAudioLead     float64 // seconds between the dub's first video frame and its first audio sample
	refSize          int64
}

// usageText documents the three invocation forms.
const usageText = `usage: mediatool align [flags] <reference> <dubbed> [output]
       mediatool align [flags] <reference-dir> <dubbed-dir>
       mediatool align [flags] <reference> <dubbed-dir>
       mediatool align [flags] <reference-dir> <dubbed>
       mediatool align [flags] -dub-suffix .cz <dir>
       mediatool align -clean-cache [-cache-dir dir] [-n]`

// Run executes the align subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("align", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), usageText)
		fs.PrintDefaults()
	}
	var cfg Config
	var verbose bool
	fs.Float64Var(&cfg.Interval, "interval", 0.05, "frame sampling interval in seconds (smaller = finer map, slower; 0.05 resolves lipsync to about one video frame)")
	fs.IntVar(&cfg.MatchDist, "match-dist", 45, "max hash distance (of 1024 bits) for a reliable frame match")
	fs.StringVar(&cfg.Bitrate, "bitrate", "192k", "bitrate of the resampled dub track (AAC)")
	fs.StringVar(&cfg.LangRef, "lang-ref", "eng", "expected language tag of the reference audio (sanity check; all reference tracks are copied)")
	fs.StringVar(&cfg.LangDub, "lang-dub", "ces", "language tag of the dubbed audio track to select and tag")
	fs.StringVar(&cfg.DubSuffix, "dub-suffix", "", "single-directory mode: dub files end with this stem suffix (e.g. .cz)")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.Var((*pairFlags)(&cfg.Pairs), "pair", "manual ref=dub pairing, repeatable (batch mode)")
	fs.BoolVar(&cfg.PairOnly, "pair-only", false, "batch mode: stop after pairing, do not analyse timing")
	fs.BoolVar(&cfg.NoContentPair, "no-content-pair", false, "batch mode: do not verify episode-tag pairings by video content")
	fs.BoolVar(&cfg.NoBackup, "no-backup", false, "batch mode: replace the reference and delete the dub without .old backups")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: build the timing map/plan and print it, write nothing")
	fs.StringVar(&cfg.CacheDir, "cache-dir", "", "frame-hash cache directory (default: $XDG_CACHE_HOME/mediatool/align)")
	fs.BoolVar(&cfg.NoCache, "no-cache", false, "do not read or write the frame-hash cache")
	fs.BoolVar(&cfg.CleanCache, "clean-cache", false, "remove all cached frame hashes and exit")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if cfg.Interval <= 0 {
		return fmt.Errorf("-interval must be positive")
	}
	if cfg.MatchDist < 1 || cfg.MatchDist > vhash.Side*vhash.Side {
		return fmt.Errorf("-match-dist must be in [1, %d] bits", vhash.Side*vhash.Side)
	}
	if cfg.DubSuffix != "" && cfg.DubSuffix != strings.TrimSpace(cfg.DubSuffix) {
		return fmt.Errorf("-dub-suffix must not contain surrounding whitespace")
	}
	if strings.TrimSpace(cfg.LangDub) == "" {
		return fmt.Errorf("-lang-dub must not be empty")
	}
	if cfg.CacheDir == "" {
		cfg.CacheDir = vhash.DefaultCacheDir()
	}
	if abs, err := filepath.Abs(cfg.CacheDir); err == nil {
		cfg.CacheDir = abs
	}

	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)

	if cfg.CleanCache {
		if fs.NArg() != 0 {
			return fmt.Errorf("-clean-cache takes no target")
		}
		t := &tool{cfg: &cfg, rep: rep, cache: vhash.Cache{Dir: cfg.CacheDir}}
		return t.cleanCache()
	}

	batch, err := resolveTargets(fs.Args(), &cfg)
	if err != nil {
		return err
	}

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		rep:        rep,
		cache:      vhash.Cache{Dir: cfg.CacheDir},
		guard:      run.NewTmpGuard(),
	}
	defer t.guard.Cleanup()

	if batch {
		var warnings []string
		refBase := cfg.RefDir
		if refBase == "" && cfg.Ref != "" {
			refBase = filepath.Dir(cfg.Ref)
		}
		dubBase := cfg.DubDir
		if dubBase == "" && cfg.Dub != "" {
			dubBase = filepath.Dir(cfg.Dub)
		}
		if refBase != "" {
			t.ignores, warnings = ignore.Load(refBase, "align", cfg.Ignores)
		}
		if dubBase != "" && dubBase != refBase {
			dubIgnores, dubWarnings := ignore.Load(dubBase, "align", nil)
			t.ignores = t.ignores.Merge(dubIgnores)
			warnings = append(warnings, dubWarnings...)
		}
		for _, w := range warnings {
			t.rep.Raw(ui.WarnTone, "warning: %s\n", w)
		}
		return t.runBatch()
	}
	return t.runOne()
}

// resolveTargets decides between the single-pair and batch modes from the
// positional arguments and fills the config.
func resolveTargets(args []string, cfg *Config) (batch bool, err error) {
	for i := range args {
		args[i] = expandUserPath(args[i])
	}
	if len(args) < 1 || len(args) > 3 {
		fmt.Fprintln(os.Stderr, usageText)
		return false, fmt.Errorf("align needs a reference and a dubbed file, or two directories")
	}
	switch len(args) {
	case 1:
		if cfg.DubSuffix == "" {
			fmt.Fprintln(os.Stderr, usageText)
			return false, fmt.Errorf("a single directory needs -dub-suffix (e.g. -dub-suffix .cz)")
		}
		dir, err := cli.ResolveMediaTarget(args[0], false)
		if err != nil {
			return false, err
		}
		cfg.RefDir, cfg.DubDir = dir, dir
		return true, nil
	case 2:
		refInfo, refErr := os.Stat(args[0])
		dubInfo, dubErr := os.Stat(args[1])
		if refErr != nil || dubErr != nil {
			return false, positionalError(args, refErr, dubErr)
		}
		if refInfo.IsDir() != dubInfo.IsDir() {
			// one file + one directory: search the directory for the
			// counterpart of the single file (one-pair batch semantics)
			if cfg.DubSuffix != "" {
				return false, fmt.Errorf("-dub-suffix applies to a single directory, not to a file/directory mix")
			}
			cfg.Mixed = true
			if refInfo.IsDir() {
				refDir, err := cli.ResolveMediaTarget(args[0], false)
				if err != nil {
					return false, err
				}
				dub, err := cli.ResolvePath(args[1])
				if err != nil {
					return false, err
				}
				cfg.RefDir, cfg.Dub = refDir, dub
				return true, nil
			}
			ref, err := cli.ResolvePath(args[0])
			if err != nil {
				return false, err
			}
			dubDir, err := cli.ResolveMediaTarget(args[1], false)
			if err != nil {
				return false, err
			}
			cfg.Ref, cfg.DubDir = ref, dubDir
			return true, nil
		}
		if refInfo.IsDir() {
			if cfg.DubSuffix != "" {
				return false, fmt.Errorf("-dub-suffix applies to a single directory; with two directories the dub marker is not needed")
			}
			refDir, err := cli.ResolveMediaTarget(args[0], false)
			if err != nil {
				return false, err
			}
			dubDir, err := cli.ResolveMediaTarget(args[1], false)
			if err != nil {
				return false, err
			}
			if refDir == dubDir {
				return false, fmt.Errorf("reference and dub directories are the same - use -dub-suffix to separate them inside one directory")
			}
			cfg.RefDir, cfg.DubDir = refDir, dubDir
			return true, nil
		}
		if cfg.PairOnly {
			return false, fmt.Errorf("-pair-only is a batch-mode flag")
		}
	}
	if cfg.DubSuffix != "" {
		return false, fmt.Errorf("-dub-suffix applies to a single directory, not to a file pair")
	}
	if cfg.PairOnly {
		return false, fmt.Errorf("-pair-only is a batch-mode flag")
	}
	if len(cfg.Pairs) > 0 {
		return false, fmt.Errorf("-pair is a batch-mode flag")
	}
	if cfg.NoBackup {
		return false, fmt.Errorf("-no-backup is a batch-mode flag")
	}
	if len(cfg.Ignores) > 0 {
		return false, fmt.Errorf("-i is a batch-mode flag")
	}
	for _, arg := range args {
		if info, err := os.Stat(arg); err == nil && info.IsDir() {
			if len(args) == 3 {
				return false, fmt.Errorf("an output argument is only supported with two files")
			}
			return false, fmt.Errorf("batch mode takes two directories or one file plus one directory")
		}
	}
	if cfg.Ref, err = cli.ResolvePath(args[0]); err != nil {
		return false, err
	}
	if cfg.Dub, err = cli.ResolvePath(args[1]); err != nil {
		return false, err
	}
	cfg.Out = ""
	if len(args) == 3 {
		cfg.Out = args[2]
	}
	if cfg.Out == "" {
		cfg.Out = strings.TrimSuffix(cfg.Ref, filepath.Ext(cfg.Ref)) + "+dub.mkv"
	}
	if _, err := os.Stat(cfg.Out); err == nil {
		return false, fmt.Errorf("output %s already exists", cfg.Out)
	}
	switch strings.ToLower(filepath.Ext(cfg.Out)) {
	case ".mkv", ".mp4":
	default:
		return false, fmt.Errorf("output %s must end in .mkv or .mp4", cfg.Out)
	}
	return false, nil
}

// expandUserPath expands a leading ~ to the user's home directory. The
// shell skips this when the path is quoted, which would otherwise reach
// os.Stat verbatim and look like a missing file.
func expandUserPath(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// positionalError names each positional argument that os.Stat rejected,
// instead of a generic "both arguments must exist".
func positionalError(args []string, errs ...error) error {
	var msgs []string
	for i, err := range errs {
		if err == nil {
			continue
		}
		kind := "reference"
		if i == 1 {
			kind = "dubbed"
		}
		var pe *os.PathError
		if errors.As(err, &pe) {
			err = pe.Err
		}
		msgs = append(msgs, fmt.Sprintf("%s %q: %v", kind, args[i], err))
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

// runOne aligns a single reference/dub pair into a new file; both inputs
// stay untouched.
func (t *tool) runOne() error {
	in, err := t.probeInputs(t.cfg.Ref, t.cfg.Dub)
	if err != nil {
		return err
	}
	outFmt := containerForOut(t.cfg.Out)
	if outFmt == "mp4" && !mp4Safe(in.refInfo) {
		return fmt.Errorf("output %s is MP4 but the reference carries subtitles/attachments/streams MP4 cannot copy - use a .mkv output", t.cfg.Out)
	}
	kvs := []ui.KV{
		ui.Pair("Reference", fmt.Sprintf("%s (%s)", t.cfg.Ref, clock(in.refDur))),
		ui.Pair("Dubbed", fmt.Sprintf("%s (%s)", t.cfg.Dub, clock(in.dubDur))),
		ui.Pair("Output", t.cfg.Out),
		ui.Pair("Frame interval", fmt.Sprintf("every %gs", t.cfg.Interval)),
		ui.Pair("Match distance", fmt.Sprintf("%d of 1024 bits", t.cfg.MatchDist)),
		ui.Pair("Frame cache", t.cacheLabel()),
		ui.Pair("Dub audio", t.dubAudioLabel(in)),
		ui.Pair("Ref audio", "all tracks copied"),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
	fit, err := t.buildFit(in)
	if err != nil {
		return err
	}
	t.printFit(fit, in)
	if t.cfg.DryRun {
		t.rep.Summary("Done.", "Timing fitted", "", ui.Outcome{Done: 1, Elapsed: time.Since(start)})
		return nil
	}

	t.rep.Start("Aligning", 1, 1, t.cfg.Ref, media.HumanBytes(in.refSize), time.Duration(in.refDur*float64(time.Second)),
		fmt.Sprintf("dub audio: %s", t.dubAudioLabel(in)))
	if err := t.muxAligned(in, fit, t.cfg.Out, outFmt); err != nil {
		return err
	}
	newSize := int64(0)
	if st, statErr := os.Stat(t.cfg.Out); statErr == nil {
		newSize = st.Size()
	}
	t.rep.Success("Created", t.cfg.Out, 0, newSize, "", time.Since(start))
	t.rep.Summary("Done.", "Aligned", "", ui.Outcome{Done: 1, Elapsed: time.Since(start)})
	return nil
}

// probeInputs checks both files before the expensive passes run: usable
// durations, a video stream and an audio track on each side, and the dub
// track selected by language.
func (t *tool) probeInputs(ref, dub string) (*inputs, error) {
	refInfo, err := probe.Probe(t.r, t.ffprobeBin, ref)
	if err != nil {
		return nil, fmt.Errorf("reference: %w", err)
	}
	dubInfo, err := probe.Probe(t.r, t.ffprobeBin, dub)
	if err != nil {
		return nil, fmt.Errorf("dubbed: %w", err)
	}
	refDur, ok := refInfo.Duration()
	if !ok || refDur <= 0 {
		return nil, fmt.Errorf("cannot determine the reference duration - needed to size the timing map and the resampled audio")
	}
	dubDur, ok := dubInfo.Duration()
	if !ok || dubDur <= 0 {
		return nil, fmt.Errorf("cannot determine the dubbed duration - needed to size the timing map")
	}
	if len(refInfo.ByType()[probe.TypeVideo]) == 0 {
		return nil, fmt.Errorf("%s has no video track to copy", ref)
	}
	if len(refInfo.ByType()[probe.TypeAudio]) == 0 {
		return nil, fmt.Errorf("%s has no audio track to copy", ref)
	}
	if audios := refInfo.ByType()[probe.TypeAudio]; len(audios) > 1 && !anyLang(audios, t.cfg.LangRef) {
		t.rep.Note(fmt.Sprintf("note: %s has no '%s' audio track among %d tracks - keeping all of them", ref, t.cfg.LangRef, len(audios)))
	}
	dubAudio, matched := selectDubAudio(dubInfo, t.cfg.LangDub)
	if len(dubInfo.ByType()[probe.TypeAudio]) == 0 {
		return nil, fmt.Errorf("%s has no audio track to align", dub)
	}
	in := &inputs{
		ref:            ref,
		dub:            dub,
		refInfo:        refInfo,
		dubInfo:        dubInfo,
		refDur:         refDur,
		dubDur:         dubDur,
		dubAudio:       dubAudio,
		dubLangMatched: matched,
		dubAudioLead:   audioLead(dubInfo, dubAudio),
	}
	if st, statErr := os.Stat(ref); statErr == nil {
		in.refSize = st.Size()
	}
	if in.dubAudioLead >= 0.05 {
		t.rep.Note(fmt.Sprintf("note: the dubbed audio starts %.2fs after its first video frame - keeping that lead as silence", in.dubAudioLead))
	}
	if nAttach := len(refInfo.ByType()["attachment"]); nAttach > 0 {
		t.rep.Note(fmt.Sprintf("note: the reference carries %d attachment stream(s) (e.g. fonts), copied when the output is MKV", nAttach))
	}
	return in, nil
}

// selectDubAudio picks the dub track: the first language match (then the
// widest/highest-bitrate one), or the first audio track when no tag
// matches.
func selectDubAudio(info *probe.Info, lang string) (probe.Typed, bool) {
	audios := info.ByType()[probe.TypeAudio]
	if len(audios) == 0 {
		return probe.Typed{}, false
	}
	wanted := media.ExpandWhitelist([]string{lang})
	var matched []probe.Typed
	for _, a := range audios {
		if media.LangMatches(a.Stream.Tags.Language, wanted) {
			matched = append(matched, a)
		}
	}
	if len(matched) == 0 {
		return audios[0], false
	}
	best := matched[0]
	for _, a := range matched[1:] {
		if a.Stream.Channels > best.Stream.Channels ||
			(a.Stream.Channels == best.Stream.Channels && a.Stream.BitRateInt() > best.Stream.BitRateInt()) {
			best = a
		}
	}
	return best, true
}

// audioLead returns the gap between the dub's first video frame and its
// first audio sample. Frame sampling starts at the first video frame and
// raw audio extraction drops leading silence, so the resampler must
// subtract this lead from dubbed video time. The container origin is only
// a fallback: it can be earlier than the video (e.g. a subtitle stream).
func audioLead(info *probe.Info, audio probe.Typed) float64 {
	origin := 0.0
	if v, ok := info.Format.StartSeconds(); ok {
		origin = v
	}
	if videos := info.ByType()[probe.TypeVideo]; len(videos) > 0 {
		for _, v := range videos {
			if media.IsCoverArt(v.Stream) {
				continue
			}
			if t, ok := v.Stream.StartSeconds(); ok {
				origin = t
			}
			break
		}
	}
	first, ok := audio.Stream.StartSeconds()
	if !ok {
		return 0
	}
	lead := first - origin
	if lead <= 0.001 || lead > 10 {
		return 0
	}
	return lead
}

// anyLang reports whether any audio track carries the requested language.
func anyLang(tracks []probe.Typed, lang string) bool {
	wanted := media.ExpandWhitelist([]string{lang})
	for _, a := range tracks {
		if media.LangMatches(a.Stream.Tags.Language, wanted) {
			return true
		}
	}
	return false
}

// dubAudioLabel renders the selected track for headers and plan lines.
func (t *tool) dubAudioLabel(in *inputs) string {
	s := fmt.Sprintf("#%d %s", in.dubAudio.Ordinal, in.dubAudio.Stream.CodecName)
	if in.dubAudio.Stream.Channels > 0 {
		s += fmt.Sprintf(" %dch", in.dubAudio.Stream.Channels)
	}
	lang := in.dubAudio.Stream.Tags.Language
	if lang == "" {
		lang = "no lang tag"
	}
	s += " (" + lang + ")"
	if !in.dubLangMatched {
		s += ", no '" + t.cfg.LangDub + "' track found - using the first audio"
	}
	return s
}

// buildFit hashes both files and fits the timing map.
func (t *tool) buildFit(in *inputs) (*timingMap, error) {
	refHashes, err := t.hashFrames(in.ref, "reference")
	if err != nil {
		return nil, fmt.Errorf("reference: %w", err)
	}
	dubHashes, err := t.hashFrames(in.dub, "dubbed")
	if err != nil {
		return nil, fmt.Errorf("dubbed: %w", err)
	}
	slog.Debug("frames", "ref", len(refHashes), "dub", len(dubHashes))

	// self-similarity guard: on synthetic or static content a match is
	// barely better evidence than chance, however small its distance
	minFrames := max(1, int(math.Ceil(5.0/t.cfg.Interval)))
	bgRef := vhash.BackgroundDistance(refHashes, minFrames, 200)
	bgDub := vhash.BackgroundDistance(dubHashes, minFrames, 200)
	bg := minPositive(bgRef, bgDub)
	if bg >= 0 && bg < vhash.SelfSimilarBits {
		t.rep.Note(fmt.Sprintf("note: frames are highly self-similar (background median %d of %d bits) - scene matches carry little evidence, verify the timing report carefully", bg, vhash.Side*vhash.Side))
	}

	return fitTiming(refHashes, dubHashes, in.refDur, in.dubDur, t.cfg.Interval, t.cfg.MatchDist)
}

// cacheLabel renders the frame cache setting for the run header.
func (t *tool) cacheLabel() string {
	if t.cfg.NoCache {
		return "off (-no-cache)"
	}
	if t.cfg.CacheDir == "" {
		return "off"
	}
	return t.cfg.CacheDir
}

// hashFrames loads a file's frame signatures from the cache or extracts
// and stores them. A cache failure never fails the alignment.
func (t *tool) hashFrames(path, label string) ([][vhash.HashLen]byte, error) {
	if !t.cfg.NoCache {
		if hashes, ok := t.cache.Load(path, t.cfg.Interval); ok {
			t.rep.Phase("using cached %s frames...", label)
			slog.Debug("frame cache hit", "path", path, "frames", len(hashes))
			return hashes, nil
		}
	}
	t.rep.Phase("extracting %s frames (decodes the whole file)...", label)
	hashes, err := vhash.Extract(t.r, t.ffmpegBin, path, t.cfg.Interval)
	if err != nil {
		return nil, err
	}
	if !t.cfg.NoCache {
		if err := t.cache.Store(path, t.cfg.Interval, hashes); err != nil {
			slog.Warn("cannot write the frame cache", "path", path, "err", err)
		}
	}
	return hashes, nil
}

// cleanCache removes every cached frame signature (or reports them in a
// dry run). No media file is touched.
func (t *tool) cleanCache() error {
	start := time.Now()
	cache := vhash.Cache{Dir: t.cfg.CacheDir}
	files, size, err := cache.Stats()
	if err != nil {
		return fmt.Errorf("cannot read the frame cache: %w", err)
	}
	kvs := []ui.KV{
		ui.Pair("Cache dir", t.cfg.CacheDir),
		ui.Pair("Cache files", fmt.Sprint(files)),
		ui.Pair("Cache size", media.HumanBytes(size)),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)
	if files == 0 {
		t.rep.Raw(ui.Neutral, "No cached frame hashes found.\n")
		t.rep.Summary("Done.", "Removed", "", ui.Outcome{Elapsed: time.Since(start)})
		return nil
	}
	if t.cfg.DryRun {
		t.rep.Summary("Done.", "Would remove", "", ui.Outcome{Done: files, Elapsed: time.Since(start)})
		return nil
	}
	removed, freed, err := cache.Clear()
	if err != nil {
		return fmt.Errorf("cannot clean the frame cache: %w", err)
	}
	t.rep.Summary("Done.", "Removed", "", ui.Outcome{Done: removed, BytesIn: freed, Elapsed: time.Since(start)})
	return nil
}

// printFit renders the fitted timing map: the speed with its
// known-conversion label, the segments with their offsets, the silent
// gaps (removed scenes) and an offset table.
func (t *tool) printFit(m *timingMap, in *inputs) {
	line := fmt.Sprintf("Timing: speed %.5f", m.Speed)
	if note := speedNote(m.Speed); note != "" {
		line += " (" + note + ")"
	}
	line += fmt.Sprintf(", %d segment(s)", len(m.Segments))
	t.rep.Raw(ui.GoodTone, "%s\n", line)
	t.rep.Raw(ui.Neutral, "  durations: ref %s, dub %s (ratio %.4f vs fitted speed %.5f)\n",
		clock(in.refDur), clock(in.dubDur), m.DurRatio, m.Speed)
	if d1 := math.Abs(m.Speed - 1); d1 > directionTol {
		if d2 := math.Abs(m.DurRatio - 1); d2 > directionTol && (m.Speed-1)*(m.DurRatio-1) < 0 {
			t.rep.Note("note: the duration ratio points the opposite way from the fitted speed - a modified intro/credits or a different cut explains this, but verify the pairing")
		}
	}
	t.rep.Raw(ui.Neutral, "  %d of %d dub frames in the shared span match (%.0f%%), span support %.0f%%\n",
		m.NInliers, m.NFine, m.InlierFrac*100, m.SpanSupport*100)
	t.rep.Raw(ui.Neutral, "  median residual %dms, p95 %dms, sampling grid %dms\n",
		int(math.Round(m.MedianResid*1000)), int(math.Round(m.P95Resid*1000)), int(math.Round(t.cfg.Interval*1000)))
	for i, s := range m.Segments {
		t.rep.Raw(ui.Neutral, "  segment %d: dub %s-%s -> ref %s-%s (offset %+.2fs, %d matches, residual %dms)\n",
			i+1, clock(s.DubStart), clock(s.DubEnd), clock(s.RefStart), clock(s.RefEnd),
			s.refAt(s.DubStart)-s.DubStart, s.Matches, int(math.Round(s.Residual*1000)))
		// a gap before the reference end is the silent tail, reported below
		if i+1 < len(m.Segments) {
			if gap := m.Segments[i+1].RefStart - s.RefEnd; gap > 0.5 {
				t.rep.Raw(ui.WarnTone, "  gap: ref %s-%s (%.1fs) has no dub audio (cut in the dub?)\n",
					clock(s.RefEnd), clock(m.Segments[i+1].RefStart), gap)
			}
		}
	}
	// reference regions with no dub counterpart stay silent on the dub
	// track; dub regions without a reference counterpart are dropped.
	// Spans under half a second are fade noise and not reported.
	if head := m.Segments[0].RefStart; head > 0.5 {
		t.rep.Raw(ui.WarnTone, "  silent head: %s of the reference has no dub audio\n", clock(head))
	}
	if head := m.Segments[0].DubStart; head > 0.5 {
		t.rep.Raw(ui.WarnTone, "  dropped head: the dub's first %s have no reference counterpart\n", clock(head))
	}
	last := m.Segments[len(m.Segments)-1]
	if tail := in.refDur - last.RefEnd; tail > 0.5 {
		t.rep.Raw(ui.WarnTone, "  silent tail: %s of the reference after the dub has no dub audio\n", clock(tail))
	}
	if drop := in.dubDur - last.dubAt(last.RefEnd); drop > 0.5 && last.RefEnd >= in.refDur-0.5 {
		t.rep.Raw(ui.WarnTone, "  dropped tail: the dub's last %s run past the reference end\n", clock(drop))
	}
	if shortest := math.Min(in.refDur, in.dubDur); shortest > 0 && m.Overlap < warnOverlap*shortest {
		t.rep.Note(fmt.Sprintf("note: the files share only %.0f%% of their content (modified intro/credits or a different cut)", m.Overlap/shortest*100))
	}
	if t.cfg.DryRun {
		t.rep.Note("note: the dub audio's offset against the reference audio is measured and corrected when muxing")
	}
	// consistency: the duration difference must be explained by the
	// silent ends and the detected cuts - a mismatch is the fingerprint
	// of smaller cuts the detection missed, which would leave drift
	gapTotal := 0.0
	insertDub := 0.0
	for i := 0; i+1 < len(m.Segments); i++ {
		gapTotal += m.Segments[i+1].RefStart - m.Segments[i].RefEnd
		insertDub += max(0, m.Segments[i+1].DubStart-m.Segments[i].DubEnd)
	}
	droppedHead := m.Segments[0].DubStart
	droppedTail := max(0, in.dubDur-last.DubEnd)
	detected := m.Segments[0].RefStart + (in.refDur - last.RefEnd) + gapTotal -
		m.Speed*(droppedHead+droppedTail+insertDub)
	if unexplained := in.refDur - m.Speed*in.dubDur - detected; math.Abs(unexplained) > 1.0 {
		t.rep.Note(fmt.Sprintf("note: the duration difference leaves %.1fs unexplained by the detected cuts - smaller cuts may have been missed (check the offsets below for drift)", unexplained))
	}
	step := max(1, (int(in.dubDur)+9)/10)
	for s := 0; s <= int(in.dubDur); s += step {
		if ref, ok := m.refAtDub(float64(s)); ok && ref >= 0 && ref <= in.refDur {
			t.rep.Raw(ui.Neutral, "  dub %5ds -> ref %8.2fs  (offset %+.2fs)\n", s, ref, ref-float64(s))
		} else {
			t.rep.Raw(ui.Neutral, "  dub %5ds -> (outside the reference)\n", s)
		}
	}
}

// resampleAgain rewrites the resampled dub after the audio calibration
// shifted the map's intercepts.
func (t *tool) resampleAgain(dubRaw, resRaw string, m *timingMap, in *inputs) error {
	src, err := os.Open(dubRaw)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.Create(resRaw)
	if err != nil {
		return err
	}
	defer dst.Close()
	if err := resampleDub(src, dst, m, in.refDur, in.dubAudioLead); err != nil {
		return err
	}
	return dst.Close()
}

// muxAligned extracts the dubbed audio, resamples it onto the reference
// timeline and muxes every reference stream plus the new track.
func (t *tool) muxAligned(in *inputs, m *timingMap, outPath, outFmt string) error {
	work, err := os.MkdirTemp("", "mediatool-align-")
	if err != nil {
		return err
	}
	t.guard.Add(work)
	defer func() {
		t.guard.Remove(work)
		os.RemoveAll(work)
	}()
	dubRaw := filepath.Join(work, "dub.raw")
	resRaw := filepath.Join(work, "dub_resampled.raw")

	t.rep.Phase("extracting the dubbed audio...")
	if _, err := t.r.Stream(t.ffmpegBin, "-v", "error", "-y", "-i", in.dub,
		"-map", fmt.Sprintf("0:a:%d", in.dubAudio.Ordinal),
		"-ac", "2", "-ar", fmt.Sprint(sampleRate), "-f", "s16le", dubRaw); err != nil {
		return fmt.Errorf("cannot extract the dubbed audio: %w", err)
	}

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
	if err := resampleDub(src, dst, m, in.refDur, in.dubAudioLead); err != nil {
		dst.Close()
		return err
	}
	if err := dst.Close(); err != nil {
		return err
	}

	// the frame map cannot see the dub file's own audio/video offset, so
	// it is measured against the reference audio (shared music/effects
	// bed) and the map's intercepts shift by it before the final resample
	t.rep.Phase("calibrating the audio offset...")
	if err := t.calibrateAudio(in, m, dubRaw, resRaw); err != nil {
		t.rep.Note(fmt.Sprintf("note: audio calibration skipped: %v", err))
	} else {
		if err := t.resampleAgain(dubRaw, resRaw, m, in); err != nil {
			return err
		}
	}

	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y",
		"-i", in.ref,
		"-f", "s16le", "-ar", fmt.Sprint(sampleRate), "-ac", "2", "-i", resRaw,
		"-map", "0", "-map", "1:a:0",
		"-map_metadata", "0", "-map_chapters", "0",
		"-c", "copy"}
	audioOrdinal := len(in.refInfo.ByType()[probe.TypeAudio])
	args = append(args,
		fmt.Sprintf("-c:a:%d", audioOrdinal), "aac",
		fmt.Sprintf("-b:a:%d", audioOrdinal), t.cfg.Bitrate,
		fmt.Sprintf("-metadata:s:a:%d", audioOrdinal), "language="+t.cfg.LangDub,
		fmt.Sprintf("-metadata:s:a:%d", audioOrdinal), "title=Dubbed (resampled)")
	if outFmt == "mp4" {
		// MP4 has no per-track title; the handler name carries the marker.
		args = append(args, fmt.Sprintf("-metadata:s:a:%d", audioOrdinal), "handler_name=Dubbed (resampled)")
	}
	args = append(args,
		fmt.Sprintf("-disposition:a:%d", audioOrdinal), "0",
		"-max_muxing_queue_size", "4096")
	if outFmt == "mp4" {
		args = append(args, "-movflags", "+use_metadata_tags+faststart")
	}
	args = append(args, "-f", outFmt, outPath)

	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !t.cfg.Verbose {
		onStats = t.rep.Progress
	}
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, args...); err != nil {
		return fmt.Errorf("muxing failed: %w", err)
	}
	return nil
}

// containerForOut derives the muxer from the output extension.
func containerForOut(path string) string {
	if strings.EqualFold(filepath.Ext(path), ".mp4") {
		return "mp4"
	}
	return "matroska"
}

// clock renders a media duration as h:mm:ss (rounded to the nearest second).
func clock(d float64) string {
	sec := int(math.Round(d))
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

// pairFlags adapts repeated -pair values into a slice.
type pairFlags []string

func (p *pairFlags) String() string { return strings.Join(*p, ",") }

func (p *pairFlags) Set(v string) error {
	*p = append(*p, v)
	return nil
}
