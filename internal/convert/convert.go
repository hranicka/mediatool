// Package convert re-encodes H.264 video tracks to HEVC, replacing files
// in place with .old backups (or writing new _CC4_FIXED files in
// chromecast mode). Port of the bash convert-video.sh suite: quality
// targets with source-bitrate caps, HDR10/HLG preservation, Dolby Vision
// skip, cover-art removal, multi-video guard, retry ladder and safety
// gates.
package convert

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/hranicka/media-tool/internal/backup"
	"github.com/hranicka/media-tool/internal/encoders"
	"github.com/hranicka/media-tool/internal/gates"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/media"
	"github.com/hranicka/media-tool/internal/probe"
	"github.com/hranicka/media-tool/internal/run"
	"github.com/hranicka/media-tool/internal/walk"
)

// decodeErrRe counts decode-error lines in the ffmpeg log; a damaged
// source can decode with errors yet still exit 0.
var decodeErrRe = regexp.MustCompile(`(?i)invalid nal|error splitting|decoding error|co located pocs|corrupt|error while decoding`)

// Config holds the convert subcommand settings.
type Config struct {
	Dir         string
	Mode        encoders.Mode
	Enc         string
	CQ          int
	ScaleHeight int
	Ignores     []string
	Force       bool
	DryRun      bool
	VaapiDevice string
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	enc        encoders.Encoder
	cfg        *Config
	ignores    ignore.List
	out        io.Writer
	guard      *run.TmpGuard

	converted, skipped, failed int
}

type attempt struct {
	decArgs    []string
	streamArgs []string
	label      string
}

var attempts = []attempt{
	{[]string{"-hwaccel", "auto"}, []string{"-c:s", "copy", "-c:d", "copy"}, "hw decode, subtitles copied"},
	{[]string{"-hwaccel", "auto"}, []string{"-sn", "-dn"}, "hw decode, subtitles dropped"},
	// software decode is more tolerant of damaged streams than hw decoders
	{nil, []string{"-sn", "-dn"}, "software decode"},
}

// Run executes the convert subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("convert", flag.ExitOnError)
	var cfg Config
	var mode, scaleArg string
	var verbose bool
	fs.StringVar(&mode, "p", "movie", "content mode: movie (default), camera, lowq, chromecast")
	fs.StringVar(&cfg.Enc, "e", "auto", "encoder: auto (probe), nvenc, qsv, vaapi, x265")
	fs.IntVar(&cfg.CQ, "q", 0, "CQ/CRF value (defaults: movie 26, camera 28, lowq 31, chromecast 22)")
	fs.StringVar(&scaleArg, "r", "", "downscale to this height when the source is larger (e.g. 1080p); never upscales")
	fs.StringVar(&cfg.VaapiDevice, "vaapi_device", "/dev/dri/renderD128", "VAAPI render device")
	fs.Var(cliIgnoreVar{&cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.Force, "f", false, "force re-encoding of sources that are not H.264")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be done")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: media-tool convert [flags] <directory>")
		return fmt.Errorf("convert needs exactly one directory argument")
	}
	dir, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("cannot resolve directory: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("directory %s does not exist", dir)
	}
	cfg.Dir = dir

	if cfg.Mode, err = encoders.ParseMode(mode); err != nil {
		return err
	}
	if cfg.CQ == 0 {
		cfg.CQ = cfg.Mode.DefaultCQ()
	}
	if cfg.ScaleHeight, err = parseScaleHeight(scaleArg); err != nil {
		return err
	}

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	var candidates []encoders.Kind
	if cfg.Enc == "auto" {
		candidates = encoders.Candidates()
	} else {
		k, err := encoders.ParseKind(cfg.Enc)
		if err != nil {
			return err
		}
		candidates = []encoders.Kind{k}
	}

	r := run.Cmd{}
	enc, err := selectEncoder(r, os.Stdout, ffmpegBin, cfg.VaapiDevice, candidates)
	if err != nil {
		return err
	}

	return (&tool{
		r:          r,
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		enc:        enc,
		cfg:        &cfg,
		ignores:    ignore.Load(dir, "convert", cfg.Ignores),
		out:        os.Stdout,
		guard:      run.NewTmpGuard(),
	}).runAll()
}

// selectEncoder probes candidates one by one, echoing the bash progress
// lines.
func selectEncoder(r run.Runner, w io.Writer, ffmpegBin, vaapiDevice string, candidates []encoders.Kind) (encoders.Encoder, error) {
	for _, k := range candidates {
		e := encoders.Encoder{Kind: k}
		fmt.Fprintf(w, "Testing encoder: %s...\n", e.Label)
		chosen, err := encoders.Probe(r, ffmpegBin, vaapiDevice, []encoders.Kind{k})
		if err == nil {
			return chosen, nil
		}
	}
	return encoders.Encoder{}, fmt.Errorf("no working encoder among %v", candidates)
}

func parseScaleHeight(arg string) (int, error) {
	if arg == "" {
		return 0, nil
	}
	h, err := strconv.Atoi(strings.TrimSuffix(strings.ToLower(arg), "p"))
	if err != nil || h <= 0 {
		return 0, fmt.Errorf("invalid scale height %q", arg)
	}
	// round down to even (HEVC 4:2:0 needs even dimensions)
	return h / 2 * 2, nil
}

// cliIgnoreVar adapts a comma-separated flag value into a string slice.
type cliIgnoreVar struct{ dst *[]string }

func (v cliIgnoreVar) String() string { return strings.Join(*v.dst, ",") }

func (v cliIgnoreVar) Set(s string) error {
	for _, e := range strings.Split(s, ",") {
		if e != "" {
			*v.dst = append(*v.dst, e)
		}
	}
	return nil
}

func (t *tool) runAll() error {
	cfg := t.cfg
	cqNote := ""
	if cfg.Mode == encoders.ModeChromecast {
		cqNote = ", output written as new *_CC4_FIXED.mkv"
	}
	fmt.Fprintf(t.out, "FFmpeg binary : %s\n", t.ffmpegBin)
	fmt.Fprintf(t.out, "Using encoder : %s (quality value: %d)\n", t.enc.Label, cfg.CQ)
	fmt.Fprintf(t.out, "Mode          : %s%s\n", cfg.Mode, cqNote)
	fmt.Fprintf(t.out, "Target        : %s\n", cfg.Dir)
	fmt.Fprintf(t.out, "Ignore entries: %d\n", t.ignores.Len())
	if cfg.ScaleHeight > 0 {
		fmt.Fprintf(t.out, "Scale target  : %dp (downscale only)\n", cfg.ScaleHeight)
	}
	if cfg.DryRun {
		fmt.Fprintf(t.out, "Extra         : DRY RUN\n")
	}
	fmt.Fprintln(t.out, "==========================================")

	files, err := walk.Media(cfg.Dir)
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

	fmt.Fprintln(t.out, "==========================================")
	fmt.Fprintf(t.out, "Done. Converted: %d, skipped: %d, failed: %d.\n", t.converted, t.skipped, t.failed)
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

func (t *tool) skip(format string, args ...any) {
	// every call site passes the path as the last argument, filling the
	// trailing %s after the reason
	fmt.Fprintf(t.out, "Skipping ("+format+"): %s\n", args...)
	t.skipped++
}

func (t *tool) processFile(path string) {
	cfg := t.cfg

	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		fmt.Fprintf(t.out, "Skipping (cannot probe: %v): %s\n", err, path)
		t.skipped++
		return
	}

	// only one real (non-cover-art) video stream is convertible; a second
	// one means 3D MVC or similar, which a re-encode would break
	byType := info.ByType()
	var real []probe.Typed
	var cover []int
	for _, tv := range byType[probe.TypeVideo] {
		if media.IsCoverArt(tv.Stream) {
			cover = append(cover, tv.Ordinal)
		} else {
			real = append(real, tv)
		}
	}
	if len(real) != 1 {
		if len(real) == 0 {
			if len(byType[probe.TypeVideo]) > 0 {
				t.skip("no usable video stream, only cover art%s", path)
			} else {
				t.skip("no usable video stream%s", path)
			}
		} else {
			t.skip("multiple video streams, e.g. 3D MVC - not safe to re-encode%s", path)
		}
		return
	}
	v := real[0].Stream

	// downscale only when the source is taller than the target
	var vfParts []string
	scaleInfo := ""
	if cfg.ScaleHeight > 0 {
		if v.Height > cfg.ScaleHeight {
			vfParts = append(vfParts, fmt.Sprintf("scale=-2:%d:flags=lanczos", cfg.ScaleHeight))
			scaleInfo = fmt.Sprintf("downscale %dx%d -> %dp", v.Width, v.Height, cfg.ScaleHeight)
		} else {
			scaleInfo = fmt.Sprintf("size %dx%d, no downscale to %dp", v.Width, v.Height, cfg.ScaleHeight)
		}
	}
	vfParts = append(vfParts, t.enc.Filters(v.Is10Bit())...)

	// Dolby Vision: hardware HEVC encoders cannot rewrite the DV RPU
	// layer, a re-encode downgrades the file to plain HDR10/SDR
	if info.IsDolbyVision() && !cfg.Force {
		t.skip("Dolby Vision would be lost in re-encode%s", path)
		return
	}
	if info.IsDolbyVision() {
		fmt.Fprintf(t.out, "Forcing re-encode of Dolby Vision source (DV layer WILL be lost): %s\n", path)
	}
	if info.IsHDR10Plus() {
		fmt.Fprintf(t.out, "  note: HDR10+ dynamic metadata will be dropped, static HDR10 stays: %s\n", path)
	}

	var outFmt string
	var muxArgs []string
	var outPath string
	switch cfg.Mode {
	case encoders.ModeChromecast:
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_CC4_FIXED.mkv") {
			t.skip("already a Chromecast conversion%s", path)
			return
		}
		outPath = filepath.Join(filepath.Dir(path), strings.TrimSuffix(base, filepath.Ext(path))+"_CC4_FIXED.mkv")
		if _, err := os.Stat(outPath); err == nil {
			t.skip("output %s already exists%s", path, outPath)
			return
		}
		outFmt = "matroska"
	default:
		backupPath := path + ".old"
		if _, err := os.Stat(backupPath); err == nil {
			t.skip("backup %s already exists%s", path, backupPath)
			return
		}
		if v.CodecName != "h264" && !cfg.Force {
			t.skip("video codec is '%s', not h264%s", path, v.CodecName)
			return
		}
		if v.CodecName != "h264" {
			fmt.Fprintf(t.out, "Forcing re-encode of %s source: %s\n", v.CodecName, path)
		}
		outPath = path
		switch strings.ToLower(filepath.Ext(path)) {
		case ".mp4":
			outFmt = "mp4"
			muxArgs = []string{"-movflags", "+use_metadata_tags"}
		default:
			outFmt = "matroska"
		}
	}

	plan := cfg.Mode.PlanInfo(cfg.CQ, 0)
	srcSize := int64(0)
	if s, ok := info.Size(); ok {
		srcSize = s
	}
	if st, err := os.Stat(path); err == nil {
		srcSize = st.Size()
	}

	if cfg.DryRun {
		if cfg.Mode == encoders.ModeChromecast {
			fmt.Fprintf(t.out, "Would create: %s (source %s%s)\n", outPath, media.HumanBytes(srcSize), scaleSuffix(scaleInfo))
		} else {
			fmt.Fprintf(t.out, "Would convert: %s (%s, %s%s)\n", path, media.HumanBytes(srcSize), plan, scaleSuffix(scaleInfo))
		}
		t.converted++
		return
	}

	// bitrate-capped modes target a fraction of the source video bitrate
	// per file; the plan line is computed once the bitrate is known
	srcKbps := sourceVideoKbps(real[0].Stream, info, srcSize)
	plan = cfg.Mode.PlanInfo(cfg.CQ, srcKbps)

	fmt.Fprintf(t.out, "Converting: %s (%s)\n", path, media.HumanBytes(srcSize))
	fmt.Fprintf(t.out, "  plan: %s%s\n", plan, scaleSuffix(scaleInfo))
	switch v.ColorTransfer {
	case "smpte2084":
		fmt.Fprintln(t.out, "  HDR10 source: mastering display + light level metadata preserved")
	case "arib-std-b67":
		fmt.Fprintln(t.out, "  HLG source: color metadata preserved")
	}

	tmp := fmt.Sprintf("%s.%d.tmp", outPath, os.Getpid())
	t.guard.Add(tmp)
	defer func() {
		t.guard.Remove(tmp)
		if _, err := os.Stat(tmp); err == nil {
			os.Remove(tmp)
		}
	}()

	var lastStderr []byte
	for i, a := range attempts {
		fmt.Fprintf(t.out, "  encoding with %s (attempt %d/%d: %s)...\n", t.enc.Label, i+1, len(attempts), a.label)
		args := t.encodeArgs(path, tmp, outFmt, muxArgs, cover, vfParts, v, srcKbps, a)
		stderr, err := t.r.Stream(t.ffmpegBin, args...)
		if err == nil {
			lastStderr = stderr
			break
		}
		lastStderr = stderr
		os.Remove(tmp)
		slog.Debug("encode attempt failed", "path", path, "attempt", i+1, "err", err)
		fmt.Fprintf(t.out, "  attempt %d (%s) failed...\n", i+1, a.label)
		if i == len(attempts)-1 {
			fmt.Fprintf(t.out, "Error: failed to convert %s (left untouched)\n", path)
			t.failed++
			return
		}
	}

	// damaged sources decode with errors yet still exit 0; the captured
	// log tells how bad it was
	if n := countDecodeErrors(lastStderr); n > 0 {
		fmt.Fprintf(t.out, "  warning: source has damaged video data (%d decode errors); output may glitch at those spots\n", n)
	}

	if cfg.Mode == encoders.ModeChromecast {
		if err := os.Rename(tmp, outPath); err != nil {
			fmt.Fprintf(t.out, "Error: cannot finalize %s: %v\n", outPath, err)
			t.failed++
			return
		}
		fmt.Fprintf(t.out, "Created: %s (original kept)\n", outPath)
		t.converted++
		return
	}

	t.verifyAndSwap(path, tmp, info, srcSize)
}

func scaleSuffix(scaleInfo string) string {
	if scaleInfo == "" {
		return ""
	}
	return ", " + scaleInfo
}

// encodeArgs assembles one ffmpeg invocation for the current attempt.
func (t *tool) encodeArgs(src, dst, outFmt string, muxArgs []string, cover []int, vfParts []string, v probe.Stream, srcKbps int, a attempt) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y"}
	args = append(args, a.decArgs...)
	args = append(args, "-i", src)
	args = append(args, "-map", "0", "-map_metadata", "0", "-map_chapters", "0")
	for _, o := range cover {
		args = append(args, "-map", fmt.Sprintf("-0:v:%d", o))
	}
	if len(vfParts) > 0 {
		args = append(args, "-vf", strings.Join(vfParts, ","))
	}
	args = append(args, t.enc.VideoArgs(t.cfg.Mode, t.cfg.CQ, srcKbps)...)
	args = append(args, t.enc.PixFmtArgs(v.Is10Bit())...)
	args = append(args, colorArgs(v)...)
	args = append(args, "-c:a", "copy")
	args = append(args, a.streamArgs...)
	args = append(args, muxArgs...)
	return append(args, "-f", outFmt, dst)
}

// colorArgs passes known color signaling through to the encoder.
func colorArgs(v probe.Stream) []string {
	var args []string
	for _, kv := range []struct{ flag, val string }{
		{"-color_primaries", v.ColorPrimaries},
		{"-color_trc", v.ColorTransfer},
		{"-colorspace", v.ColorSpace},
	} {
		switch kv.val {
		case "", "unknown", "unspecified", "reserved":
		default:
			args = append(args, kv.flag, kv.val)
		}
	}
	return args
}

func countDecodeErrors(log []byte) int {
	n := 0
	for _, line := range strings.Split(string(log), "\n") {
		if decodeErrRe.MatchString(line) {
			n++
		}
	}
	return n
}

// sourceVideoKbps determines the source video bitrate: video stream first,
// then the container, then size/duration (bash probe_src_bitrate_kbps).
func sourceVideoKbps(v probe.Stream, info *probe.Info, srcSize int64) int {
	if br := v.BitRateInt(); br > 0 {
		return br / 1000
	}
	if br, err := strconv.Atoi(info.Format.BitRate); err == nil && br > 0 {
		return br / 1000
	}
	if dur, ok := info.Duration(); ok && dur > 0 && srcSize > 0 {
		return int(float64(srcSize) * 8 / dur / 1000)
	}
	return 0
}

// verifyAndSwap applies the safety gates and swaps the file in place with a
// .old backup.
func (t *tool) verifyAndSwap(path, tmp string, info *probe.Info, srcSize int64) {
	srcDur, srcOK := info.Duration()
	tmpInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		fmt.Fprintf(t.out, "Error: cannot probe the output: %v\n", err)
		t.failed++
		return
	}
	newDur, newOK := tmpInfo.Duration()
	if srcOK && newOK && !gates.DurationKept(newDur, srcDur, 0.99) {
		fmt.Fprintf(t.out, "Error: output (%.3fs) is much shorter than source (%.3fs) - damaged source, keeping original\n", newDur, srcDur)
		t.failed++
		return
	}

	st, err := os.Stat(tmp)
	if err != nil {
		fmt.Fprintf(t.out, "Error: output is missing: %v\n", err)
		t.failed++
		return
	}
	if !gates.Shrunk(st.Size(), srcSize) {
		fmt.Fprintf(t.out, "Skipping (re-encode is not smaller: %s -> %s)%s\n", media.HumanBytes(srcSize), media.HumanBytes(st.Size()), path)
		t.skipped++
		return
	}

	if _, err := backup.Swap(path, tmp); err != nil {
		fmt.Fprintf(t.out, "Error: %v\n", err)
		t.failed++
		return
	}
	fmt.Fprintf(t.out, "Converted: %s (%s -> %s, backup: %s.old)\n", path, media.HumanBytes(srcSize), media.HumanBytes(st.Size()), path)
	t.converted++
}
