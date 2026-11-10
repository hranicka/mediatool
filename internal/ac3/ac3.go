// Package ac3 appends AC3 640k compatibility tracks for audio in
// DTS/TrueHD/FLAC that has no AC3 track of the same language. Port of the
// bash ac3-fallback.sh (itself a port of mediatool's ac3converter with one
// deliberate change: E-AC3 is never converted — both playback targets
// decode it natively).
package ac3

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hranicka/media-tool/internal/backup"
	"github.com/hranicka/media-tool/internal/cli"
	"github.com/hranicka/media-tool/internal/gates"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/media"
	"github.com/hranicka/media-tool/internal/probe"
	"github.com/hranicka/media-tool/internal/run"
	"github.com/hranicka/media-tool/internal/walk"
)

// Config holds the ac3 subcommand settings.
type Config struct {
	Target   string
	MinBR    int
	GateLang string
	Ignores  []string
	DryRun   bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	cfg        *Config
	ignores    ignore.List
	out        io.Writer
	guard      *run.TmpGuard

	converted, untouched, failed int
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
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: media-tool ac3 [flags] <directory|file>")
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

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		ignores:    ignore.Load(cli.IgnoresDir(target), "ac3", cfg.Ignores),
		out:        os.Stdout,
		guard:      run.NewTmpGuard(),
	}
	return t.runAll()
}

func (t *tool) runAll() error {
	fmt.Fprintf(t.out, "FFmpeg binary : %s\n", t.ffmpegBin)
	fmt.Fprintf(t.out, "Min AC3 valid : %d bit/s\n", t.cfg.MinBR)
	if t.cfg.GateLang != "" {
		fmt.Fprintf(t.out, "Language gate : %s\n", t.cfg.GateLang)
	}
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
	fmt.Fprintf(t.out, "Done. Converted: %d, untouched: %d, failed: %d.\n", t.converted, t.untouched, t.failed)
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

// plan holds one to-be-appended AC3 track: the audio ordinal and language.
type plan struct {
	ordinal int
	lang    string
	codec   string
}

func (t *tool) processFile(path string) {
	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		fmt.Fprintf(t.out, "Skipping (cannot probe: %v): %s\n", err, path)
		t.untouched++
		return
	}

	// classify audio: existing AC3 marks a language "valid" unless it is a
	// commentary or low-bitrate track; DTS/TrueHD/FLAC are "bad"
	valid := map[string]bool{}
	bad := map[string]plan{}
	hasGate := t.cfg.GateLang == ""
	for _, ta := range info.ByType()[probe.TypeAudio] {
		lang := strings.ToLower(ta.Stream.Tags.Language)
		if t.cfg.GateLang != "" && lang == t.cfg.GateLang {
			hasGate = true
		}
		switch ta.Stream.CodecName {
		case "ac3":
			if media.Commentary(ta.Stream.Tags.Title) {
				continue
			}
			br := ta.Stream.BitRateInt()
			if br > 0 && br < t.cfg.MinBR {
				continue
			}
			if br == 0 && ta.Stream.Channels < 6 {
				continue
			}
			valid[lang] = true
		case "dts", "truehd", "flac":
			if _, ok := bad[lang]; !ok {
				bad[lang] = plan{ordinal: ta.Ordinal, lang: lang, codec: ta.Stream.CodecName}
			}
		}
	}

	if t.cfg.GateLang != "" && !hasGate {
		fmt.Fprintf(t.out, "Skipping (no '%s' audio track): %s\n", t.cfg.GateLang, path)
		t.untouched++
		return
	}

	langs := make([]string, 0, len(bad))
	for l := range bad {
		langs = append(langs, l)
	}
	sort.Strings(langs)

	var toConvert []plan
	for _, l := range langs {
		if valid[l] {
			slog.Debug("already has AC3, skipping track", "file", path, "lang", l)
			continue
		}
		toConvert = append(toConvert, bad[l])
	}

	if len(toConvert) == 0 {
		fmt.Fprintf(t.out, "OK (nothing to convert): %s\n", path)
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
		fmt.Fprintf(t.out, "Would convert: %s (%s)\n", path, media.HumanBytes(srcSize))
		for _, p := range toConvert {
			fmt.Fprintf(t.out, "  - append AC3 640k for audio #%d (%s, lang '%s')\n", p.ordinal, p.codec, media.LangDisplay(p.lang))
		}
		t.converted++
		return
	}

	fmt.Fprintf(t.out, "Converting: %s (%s)\n", path, media.HumanBytes(srcSize))
	for _, p := range toConvert {
		fmt.Fprintf(t.out, "  - append AC3 640k for audio #%d (%s, lang '%s')\n", p.ordinal, p.codec, media.LangDisplay(p.lang))
	}

	var fmtName string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mp4":
		fmtName = "mp4"
	default:
		fmtName = "matroska"
	}

	// the appended tracks must survive: audio count grows by the plan size
	srcTotal, srcAc3 := countAudio(info)

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
	for _, p := range toConvert {
		args = append(args, fmt.Sprintf("-c:a:%d", p.ordinal), "ac3", fmt.Sprintf("-b:a:%d", p.ordinal), "640k")
	}
	args = append(args, "-c:s", "copy", "-c:d", "copy", "-max_muxing_queue_size", "4096", "-f", fmtName, tmp)
	if _, err := t.r.Stream(t.ffmpegBin, args...); err != nil {
		fmt.Fprintf(t.out, "Error: failed to convert %s (left untouched)\n", path)
		t.failed++
		return
	}

	if !t.gatesPass(path, tmp, info, srcTotal, srcAc3, len(toConvert)) {
		return
	}

	if _, err := backup.Swap(path, tmp); err != nil {
		fmt.Fprintf(t.out, "Error: %v\n", err)
		t.failed++
		return
	}
	newSize := int64(0)
	if st, err := os.Stat(path); err == nil {
		newSize = st.Size()
	}
	fmt.Fprintf(t.out, "Converted: %s (%s -> %s, backup: %s.old)\n", path, media.HumanBytes(srcSize), media.HumanBytes(newSize), path)
	t.converted++
}

func countAudio(info *probe.Info) (total, ac3 int) {
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

// gatesPass verifies that every original stream survived plus the appended
// AC3 tracks, and that no content was lost.
func (t *tool) gatesPass(path, tmp string, info *probe.Info, srcTotal, srcAc3, appended int) bool {
	newInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		fmt.Fprintf(t.out, "Error: cannot probe the output: %v\n", err)
		t.failed++
		return false
	}
	newTotal, newAc3 := countAudio(newInfo)
	if newTotal != srcTotal+appended || newAc3 != srcAc3+appended {
		fmt.Fprintf(t.out, "Error: output does not match the plan (audio %d/%d, ac3 %d/%d) - keeping original\n",
			newTotal, srcTotal+appended, newAc3, srcAc3+appended)
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
	return true
}
