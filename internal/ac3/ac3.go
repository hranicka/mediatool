// Package ac3 appends AC3 640k compatibility tracks for audio in
// DTS/TrueHD/FLAC that has no AC3 track of the same language. Port of the
// bash ac3-fallback.sh (itself a port of mediatool's ac3converter with one
// deliberate change: E-AC3 is never converted — both playback targets
// decode it natively).
package ac3

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
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

// Config holds the ac3 subcommand settings.
type Config struct {
	Target   string
	MinBR    int
	GateLang string
	Ignores  []string
	DryRun   bool
	Verbose  bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	rep        *ui.Reporter
	guard      *run.TmpGuard

	converted, untouched, failed int
	bytesIn, bytesOut            int64
	failures                     []ui.Failure
}

// fail renders the standard "Error: ..." line and records the failure for
// the summary.
func (t *tool) fail(path, msg string) {
	t.rep.Fail(msg, "")
	t.failures = append(t.failures, ui.Failure{Path: path, Msg: msg})
	t.failed++
}

// Run executes the ac3 subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("ac3", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.IntVar(&cfg.MinBR, "b", 448000, "minimal bitrate (bit/s, default 448000 = 448 kbps) of an existing AC3 track to count as a valid conversion")
	fs.StringVar(&cfg.GateLang, "l", "", "only process files containing at least one audio track of this language")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be converted")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool ac3 [flags] <directory|file>")
		return fmt.Errorf("ac3 needs exactly one directory or file argument")
	}
	target, err := cli.ResolveMediaTarget(fs.Arg(0), true)
	if err != nil {
		return err
	}
	cfg.Target = target
	cfg.GateLang = strings.ToLower(cfg.GateLang)

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	ignores, warnings := ignore.Load(cli.IgnoresDir(target), "ac3", cfg.Ignores)
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
		ignores:    ignores,
		rep:        rep,
		guard:      run.NewTmpGuard(),
	}
	return t.runAll()
}

func (t *tool) runAll() error {
	kvs := []ui.KV{
		ui.Pair("FFmpeg binary", t.ffmpegBin),
		ui.Pair("Min AC3 valid", fmt.Sprintf("%d bit/s", t.cfg.MinBR)),
	}
	if t.cfg.GateLang != "" {
		kvs = append(kvs, ui.Pair("Language gate", t.cfg.GateLang))
	}
	kvs = append(kvs, ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())))
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

	t.rep.Summary("Done.", "Converted", "untouched", ui.Outcome{
		Done:     t.converted,
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

// PlanEntry is one DTS/TrueHD/FLAC audio track that needs an appended AC3
// compatibility copy: the per-type audio ordinal (ffmpeg's :N specifier),
// its language tag and its source codec.
type PlanEntry struct {
	Ordinal int
	Lang    string
	Codec   string
}

// PlanCompat classifies a probed file's audio tracks with the ac3
// command's rules: an existing AC3 track marks a language valid unless it
// is a commentary or below minBR (bit/s; a bitrate-less AC3 with fewer
// than 6 channels never counts). Returns one entry per DTS/TrueHD/FLAC
// language without a valid same-language AC3, ordered by language for a
// deterministic plan. Used by the ac3 command and by convert's `ac3`
// audio target.
func PlanCompat(info *probe.Info, minBR int) []PlanEntry {
	valid := map[string]bool{}
	bad := map[string]PlanEntry{}
	for _, ta := range info.ByType()[probe.TypeAudio] {
		lang := strings.ToLower(ta.Stream.Tags.Language)
		switch ta.Stream.CodecName {
		case "ac3":
			if media.Commentary(ta.Stream.Tags.Title) {
				continue
			}
			br := ta.Stream.BitRateInt()
			if br > 0 && br < minBR {
				continue
			}
			if br == 0 && ta.Stream.Channels < 6 {
				continue
			}
			valid[lang] = true
		case "dts", "truehd", "flac":
			if _, ok := bad[lang]; !ok {
				bad[lang] = PlanEntry{Ordinal: ta.Ordinal, Lang: lang, Codec: ta.Stream.CodecName}
			}
		}
	}

	langs := make([]string, 0, len(bad))
	for l := range bad {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	var out []PlanEntry
	for _, l := range langs {
		if valid[l] {
			slog.Debug("already has AC3, skipping track", "lang", l)
			continue
		}
		out = append(out, bad[l])
	}
	return out
}

// CountTracks returns the number of audio streams and how many of them
// are AC3 (the appended-track gates compare these).
func CountTracks(info *probe.Info) (total, ac3 int) {
	for _, s := range info.Streams {
		if s.CodecType != probe.TypeAudio {
			continue
		}
		total++
		if s.CodecName == "ac3" {
			ac3++
		}
	}
	return total, ac3
}

func (t *tool) processFile(i, n int, path string) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		t.rep.Skip(i, n, path, fmt.Sprintf("cannot probe: %v", err))
		t.untouched++
		return
	}

	// language gate: only files with at least one track of that language
	if t.cfg.GateLang != "" {
		hasGate := false
		for _, ta := range info.ByType()[probe.TypeAudio] {
			if strings.ToLower(ta.Stream.Tags.Language) == t.cfg.GateLang {
				hasGate = true
				break
			}
		}
		if !hasGate {
			t.rep.Skip(i, n, path, fmt.Sprintf("no '%s' audio track", t.cfg.GateLang))
			t.untouched++
			return
		}
	}

	// classify audio: DTS/TrueHD/FLAC languages without a valid same-language
	// AC3 track need a compatibility copy (see PlanCompat for the rules)
	toConvert := PlanCompat(info, t.cfg.MinBR)
	if len(toConvert) == 0 {
		t.rep.OK(i, n, path, "nothing to convert")
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

	trackLines := make([]string, 0, len(toConvert))
	for _, p := range toConvert {
		trackLines = append(trackLines, fmt.Sprintf("append AC3 640k for audio #%d (%s, lang '%s')", p.Ordinal, p.Codec, media.LangDisplay(p.Lang)))
	}

	if t.cfg.DryRun {
		t.rep.Would("Would convert", i, n, path, media.HumanBytes(srcSize), trackLines...)
		t.converted++
		return
	}

	srcDur := time.Duration(0)
	if d, ok := info.Duration(); ok {
		srcDur = time.Duration(d * float64(time.Second))
	}
	fileStart := time.Now()
	t.rep.Start("Converting", i, n, path, media.HumanBytes(srcSize), srcDur, trackLines...)

	var fmtName string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		fmtName = "mp4"
	default:
		fmtName = "matroska"
	}

	// the appended tracks must survive: audio count grows by the plan size
	srcTotal, srcAc3 := CountTracks(info)

	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	t.guard.Add(tmp)
	defer func() {
		t.guard.Remove(tmp)
		if _, err := os.Stat(tmp); err == nil {
			os.Remove(tmp)
		}
	}()

	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y", "-i", path,
		"-map", "0", "-map_metadata", "0", "-map_chapters", "0",
		"-c:v", "copy", "-c:a", "copy"}
	// appending duplicates the track: every converted stream is mapped a
	// second time and its appended copy (output audio ordinal after all
	// existing audio streams) is encoded to AC3; the original survives
	for i, p := range toConvert {
		out := srcTotal + i
		args = append(args, "-map", fmt.Sprintf("0:a:%d", p.Ordinal),
			fmt.Sprintf("-c:a:%d", out), "ac3", fmt.Sprintf("-b:a:%d", out), "640k")
	}
	args = append(args, "-c:s", "copy", "-c:d", "copy", "-max_muxing_queue_size", "4096", "-f", fmtName, tmp)
	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !t.cfg.Verbose {
		onStats = t.rep.Progress
	}
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, args...); err != nil {
		t.fail(path, fmt.Sprintf("failed to convert %s (left untouched)", path))
		return
	}

	if !t.gatesPass(path, tmp, info, srcTotal, srcAc3, len(toConvert)) {
		return
	}

	if _, err := backup.Swap(path, tmp); err != nil {
		t.fail(path, err.Error())
		return
	}
	newSize := int64(0)
	if st, err := os.Stat(path); err == nil {
		newSize = st.Size()
	}
	t.rep.Success("Converted", path, srcSize, newSize, path+".old", time.Since(fileStart))
	t.bytesIn += srcSize
	t.bytesOut += newSize
	t.converted++
}

// gatesPass verifies that every original stream survived plus the appended
// AC3 tracks, and that no content was lost.
func (t *tool) gatesPass(path, tmp string, info *probe.Info, srcTotal, srcAc3, appended int) bool {
	newInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		t.fail(path, fmt.Sprintf("cannot probe the output: %v", err))
		return false
	}
	newTotal, newAc3 := CountTracks(newInfo)
	if newTotal != srcTotal+appended || newAc3 != srcAc3+appended {
		t.fail(path, fmt.Sprintf("output does not match the plan (audio %d/%d, ac3 %d/%d) - keeping original",
			newTotal, srcTotal+appended, newAc3, srcAc3+appended))
		return false
	}
	if srcDur, ok1 := info.Duration(); ok1 {
		if newDur, ok2 := newInfo.Duration(); ok2 && !gates.DurationKept(newDur, srcDur, 0.99) {
			t.fail(path, fmt.Sprintf("output (%.3fs) is much shorter than source (%.3fs) - keeping original", newDur, srcDur))
			return false
		}
	}
	return true
}
