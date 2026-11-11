// Package convert re-encodes H.264 video tracks to HEVC (or AV1),
// replacing files in place with .old backups (or writing new _CC4_FIXED
// files in the chromecast preset). Port of the bash convert-video.sh /
// hevc-convert.sh suite: named presets bundling the common workflows,
// quality targets with source-bitrate caps, HDR10/HLG preservation or
// optional HDR->SDR tonemapping, audio targets (copy / AAC / appended AC3
// compat tracks), auto-deinterlace, Dolby Vision skip, cover-art removal,
// multi-video guard, retry ladder and safety gates.
package convert

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/ac3"
	"github.com/hranicka/mediatool/internal/backup"
	"github.com/hranicka/mediatool/internal/clean"
	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/encoders"
	"github.com/hranicka/mediatool/internal/gates"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/walk"
)

// decodeErrRe counts decode-error lines in the ffmpeg log; a damaged
// source can decode with errors yet still exit 0.
var decodeErrRe = regexp.MustCompile(`(?i)invalid nal|error splitting|decoding error|co located pocs|corrupt|error while decoding`)

// sdrColorArgs forces the output color signaling to the SDR bt709 triple;
// a tonemapped output must not carry HDR metadata.
var sdrColorArgs = []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}

// ac3MinBR mirrors the ac3 command's default validity threshold: an
// existing AC3 track counts as a valid conversion from 448 kbps up.
const ac3MinBR = 448000

// placeboTonemap is the libplacebo software tonemap filter spec (bt.2390
// EETF to SDR bt709). The availability probe runs this exact string, so a
// positive probe means the chain will actually work.
const placeboTonemap = "libplacebo=tonemapping=bt.2390:colorspace=bt709:color_primaries=bt709:color_trc=bt709:format=yuv420p"

// Config holds the convert subcommand settings.
type Config struct {
	Dir         string
	Preset      string
	Mode        encoders.Mode
	Codec       encoders.Codec
	Enc         string
	CQ          int
	ScaleHeight int
	Tonemap     bool
	Audio       string
	Langs       string
	Ignores     []string
	Force       bool
	DryRun      bool
	NoBackup    bool
	Verbose     bool
	VaapiDevice string
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	enc        encoders.Encoder
	cfg        *Config
	audio      audioTarget
	whitelist  []string
	ignores    ignore.List
	rep        *ui.Reporter
	guard      *run.TmpGuard
	placebo    *bool // memoized libplacebo filter availability

	converted, skipped, failed int
	bytesIn, bytesOut          int64
	failures                   []ui.Failure
}

// fail renders the standard "Error: ..." line and records the failure for
// the summary.
func (t *tool) fail(path, msg, detail string) {
	t.rep.Fail(msg, detail)
	t.failures = append(t.failures, ui.Failure{Path: path, Msg: msg})
	t.failed++
}

// streamGate records the stream counts the output must carry; the
// post-encode gate compares them when track cleanup or AC3 appending is
// active (subs = -1 means "not checked").
type streamGate struct {
	check       bool
	audio, subs int
	ac3         int
}

// attempt is one rung of the per-file retry ladder: the decode arguments,
// the stream policy, the full -vf chain and a human-readable label.
type attempt struct {
	decArgs    []string
	streamArgs []string
	vf         string
	label      string
}

// Run executes the convert subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("convert", flag.ExitOnError)
	var cfg Config
	var presetName, scaleArg, codec, audioSpec, langArg string
	var verbose bool
	fs.StringVar(&presetName, "p", "movie", "preset: "+presetHelp())
	fs.StringVar(&codec, "c", string(encoders.CodecHEVC), "output codec: hevc (default) or av1")
	fs.StringVar(&cfg.Enc, "e", "auto", "encoder: auto (probe), nvenc, qsv, vaapi, sw")
	fs.IntVar(&cfg.CQ, "q", 0, "CQ/CRF value (defaults: "+qualityHelp()+")")
	fs.StringVar(&scaleArg, "r", "", "downscale to this height when the source is larger (e.g. 1080p); never upscales")
	fs.StringVar(&audioSpec, "a", "", "audio target: copy, aac[:bitrate[:channels]] or ac3[:bitrate]; preset default when unset")
	fs.StringVar(&langArg, "l", "", "keep only audio/subtitle tracks of these languages (comma-separated, e.g. cs,en; like clean, sole tracks stay); unset = keep all")
	fs.BoolVar(&cfg.Tonemap, "tonemap", false, "tonemap HDR sources to SDR bt709 (preset default when unset)")
	fs.StringVar(&cfg.VaapiDevice, "vaapi_device", "/dev/dri/renderD128", "VAAPI render device")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.Force, "f", false, "force re-encoding of sources that are not H.264")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: list what would be done")
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
		fmt.Fprintln(os.Stderr, "usage: mediatool convert [flags] <directory|file>")
		return fmt.Errorf("convert needs exactly one directory or file argument")
	}
	target, err := cli.ResolveMediaTarget(fs.Arg(0), true)
	if err != nil {
		return err
	}
	cfg.Dir = target

	// the preset fills every flag the user did not pass explicitly
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	preset, err := Lookup(presetName)
	if err != nil {
		return err
	}
	cfg.Preset = presetName
	applyPreset(preset, set, &cfg, &scaleArg, &audioSpec)

	if cfg.Codec, err = encoders.ParseCodec(codec); err != nil {
		return err
	}
	if err := codecModeGuard(cfg.Codec, cfg.Mode); err != nil {
		return err
	}
	if cfg.CQ == 0 {
		cfg.CQ = cfg.Mode.DefaultCQ()
	}
	if cfg.ScaleHeight, err = parseScaleHeight(scaleArg); err != nil {
		return err
	}
	tAudio, err := parseAudio(audioSpec)
	if err != nil {
		return err
	}
	cfg.Langs = langArg
	var whitelist []string
	if langArg != "" {
		whitelist = media.ExpandWhitelist(strings.Split(langArg, ","))
	}

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	var candidates []encoders.Kind
	prof := ui.Detect(os.Stdout, os.Stderr)
	prof.Verbose = cfg.Verbose
	rep := ui.New(os.Stdout, prof)
	if cfg.Enc == "auto" {
		candidates = encoders.Candidates(cfg.Codec)
	} else {
		if cfg.Enc == "x265" {
			rep.Raw(ui.WarnTone, "warning: -e x265 is deprecated - use -e sw\n")
			cfg.Enc = "sw"
		}
		k, err := encoders.ParseKind(cfg.Enc)
		if err != nil {
			return err
		}
		candidates = []encoders.Kind{k}
	}

	r := run.Cmd{}
	enc, err := selectEncoder(r, rep, ffmpegBin, cfg.VaapiDevice, cfg.Codec, candidates)
	if err != nil {
		return err
	}

	ignores, warnings := ignore.Load(cli.IgnoresDir(target), "convert", cfg.Ignores)
	for _, w := range warnings {
		rep.Raw(ui.WarnTone, "warning: %s\n", w)
	}

	return (&tool{
		r:          r,
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		enc:        enc,
		cfg:        &cfg,
		audio:      tAudio,
		whitelist:  whitelist,
		ignores:    ignores,
		rep:        rep,
		guard:      run.NewTmpGuard(),
	}).runAll()
}

// selectEncoder probes candidates one by one, echoing the bash progress
// lines.
func selectEncoder(r run.Runner, rep *ui.Reporter, ffmpegBin, vaapiDevice string, codec encoders.Codec, candidates []encoders.Kind) (encoders.Encoder, error) {
	for _, k := range candidates {
		e := encoders.Encoder{Codec: codec, Kind: k, Label: encoders.Label(codec, k)}
		rep.Raw(ui.DimTone, "Testing encoder: %s...\n", e.Label)
		chosen, err := encoders.Probe(r, ffmpegBin, vaapiDevice, codec, []encoders.Kind{k})
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

// codecModeGuard rejects codec/mode combinations the target device cannot
// play: the chromecast preset exists for Chromecast 4 devices, which
// decode HEVC only.
func codecModeGuard(c encoders.Codec, m encoders.Mode) error {
	if m == encoders.ModeChromecast && c != encoders.CodecHEVC {
		return fmt.Errorf("chromecast mode targets Chromecast 4 devices and outputs HEVC only")
	}
	return nil
}

// convertibleSource reports whether a video codec is a standard convert
// source without -f; vc1 and wmv3 are the SMPTE VC-1 family (advanced vs
// simple/main profile) and convert like h264.
func convertibleSource(codec string) bool {
	switch codec {
	case "h264", "vc1", "wmv3":
		return true
	}
	return false
}

// isHDRSource reports whether the video track carries HDR transfer
// metadata a tonemap should re-map: HDR10, HLG and the XYZ transfer of
// Dolby Vision profile 5.
func isHDRSource(v probe.Stream) bool {
	switch v.ColorTransfer {
	case "smpte2084", "arib-std-b67", "smpte428-1":
		return true
	}
	return false
}

// needsGenpts reports whether the container family typically carries
// broken or missing PTS (MPEG-TS/PS dumps, AVI) and benefits from
// ffmpeg's genpts flag (hevc-convert.sh parity).
func needsGenpts(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".mts", ".m2ts", ".ts", ".mpg", ".mpeg", ".avi":
		return true
	}
	return false
}

// onlyVideoAudio reports whether every stream is video or audio — i.e.
// nothing (subtitles, teletext/data, attachments) that MP4 cannot carry
// would be lost by muxing to MP4 instead of MKV.
func onlyVideoAudio(info *probe.Info) bool {
	for _, s := range info.Streams {
		if s.CodecType != probe.TypeVideo && s.CodecType != probe.TypeAudio {
			return false
		}
	}
	return true
}

// withExt replaces path's extension with ext (e.g. part1.ts -> part1.mkv).
func withExt(path, ext string) string {
	return strings.TrimSuffix(path, filepath.Ext(path)) + ext
}

func (t *tool) runAll() error {
	cfg := t.cfg
	presetNote := ""
	if cfg.Mode == encoders.ModeChromecast {
		presetNote = ", output written as new *_CC4_FIXED.mkv"
	}
	// the header spells out the full effective plan, defaults included, so
	// a run is self-describing without guessing what a preset implies
	scale := "keep source resolution"
	if cfg.ScaleHeight > 0 {
		scale = fmt.Sprintf("%dp (downscale only)", cfg.ScaleHeight)
	}
	tonemap := "off (keep HDR)"
	if cfg.Tonemap {
		tonemap = "HDR sources re-map to SDR bt709"
	}
	langs := "all"
	if len(t.whitelist) > 0 {
		langs = strings.Join(t.whitelist, " ") + " (+ no/und tags)"
	}
	kvs := []ui.KV{
		ui.Pair("FFmpeg binary", t.ffmpegBin),
		ui.Pair("Using encoder", t.enc.Label),
	}
	if t.enc.Kind == encoders.VAAPI {
		kvs = append(kvs, ui.Pair("VAAPI device", cfg.VaapiDevice))
	}
	kvs = append(kvs,
		ui.Pair("Output codec", string(cfg.Codec)),
		ui.Pair("Preset", cfg.Preset+presetNote),
		ui.Pair("Quality", t.enc.PlanSummary(cfg.Mode, cfg.CQ)),
		ui.Pair("Scale", scale),
		ui.Pair("Tonemap", tonemap),
		ui.Pair("Audio target", t.audio.desc()),
		ui.Pair("Languages", langs),
	)
	if cfg.Mode != encoders.ModeChromecast {
		// chromecast writes new _CC4_FIXED.mkv files instead of swapping
		backupNote := "keep .old"
		if cfg.NoBackup {
			backupNote = "none (-no-backup)"
		}
		kvs = append(kvs, ui.Pair("Backups", backupNote))
	}
	if cfg.Force {
		kvs = append(kvs, ui.Pair("Force", "re-encode non-H.264 sources too"))
	}
	kvs = append(kvs,
		ui.Pair("Target", cfg.Dir),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())))
	if cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
	target := cfg.Dir
	st, err := os.Stat(target)
	if err != nil {
		return err
	}
	if st.IsDir() {
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

	t.rep.Summary("Done.", "Converted", "skipped", ui.Outcome{
		Done:     t.converted,
		Skipped:  t.skipped,
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
	t.skipped++
}

func (t *tool) processFile(i, n int, path string) {
	cfg := t.cfg

	info, err := probe.Probe(t.r, t.ffprobeBin, path)
	if err != nil {
		t.skip(i, n, path, fmt.Sprintf("cannot probe: %v", err))
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
				t.skip(i, n, path, "no usable video stream, only cover art")
			} else {
				t.skip(i, n, path, "no usable video stream")
			}
		} else {
			t.skip(i, n, path, "multiple video streams, e.g. 3D MVC - not safe to re-encode")
		}
		return
	}
	v := real[0].Stream

	// tonemap decision: only HDR sources change, a plain re-encode keeps HDR
	tonemapped := cfg.Tonemap && isHDRSource(v)

	// downscale only when the source is taller than the target
	scaling := cfg.ScaleHeight > 0 && v.Height > cfg.ScaleHeight
	scaleInfo := ""
	if cfg.ScaleHeight > 0 {
		if scaling {
			scaleInfo = fmt.Sprintf("downscale %dx%d -> %dp", v.Width, v.Height, cfg.ScaleHeight)
		} else {
			scaleInfo = fmt.Sprintf("size %dx%d, no downscale to %dp", v.Width, v.Height, cfg.ScaleHeight)
		}
	}

	// Dolby Vision: hardware HEVC encoders cannot rewrite the DV RPU
	// layer, a re-encode downgrades the file to plain HDR10/SDR
	if info.IsDolbyVision() && !cfg.Force {
		t.skip(i, n, path, "Dolby Vision would be lost in re-encode")
		return
	}
	if info.IsDolbyVision() {
		t.rep.Notice(i, n, path, "Forcing re-encode of Dolby Vision source (DV layer WILL be lost)")
	}
	if info.IsHDR10Plus() {
		t.rep.Note(fmt.Sprintf("  note: HDR10+ dynamic metadata will be dropped, static HDR10 stays: %s", path))
	}

	var outFmt string
	var muxArgs []string
	var outPath string
	switch cfg.Mode {
	case encoders.ModeChromecast:
		base := filepath.Base(path)
		if strings.HasSuffix(base, "_CC4_FIXED.mkv") {
			t.skip(i, n, path, "already a Chromecast conversion")
			return
		}
		outPath = filepath.Join(filepath.Dir(path), strings.TrimSuffix(base, filepath.Ext(path))+"_CC4_FIXED.mkv")
		if _, err := os.Stat(outPath); err == nil {
			t.skip(i, n, path, fmt.Sprintf("output %s already exists", outPath))
			return
		}
		outFmt = "matroska"
	default:
		if !cfg.NoBackup {
			backupPath := path + ".old"
			if _, err := os.Stat(backupPath); err == nil {
				t.skip(i, n, path, fmt.Sprintf("backup %s already exists", backupPath))
				return
			}
		}
		if !convertibleSource(v.CodecName) && !cfg.Force {
			t.skip(i, n, path, fmt.Sprintf("video codec is '%s', not convertible (want h264 or vc1)", v.CodecName))
			return
		}
		if !convertibleSource(v.CodecName) {
			t.rep.Notice(i, n, path, fmt.Sprintf("Forcing re-encode of %s source", v.CodecName))
		}
		// container rule: MKV stays MKV; other sources mux to MP4 only when
		// they carry nothing MP4 would lose (video+audio only), otherwise to
		// MKV to preserve subtitles/teletext/attachments. The output extension
		// follows the container; the original is kept as <source>.old.
		switch {
		case strings.EqualFold(filepath.Ext(path), ".mkv"):
			outFmt = "matroska"
			outPath = path
		case onlyVideoAudio(info):
			outFmt = "mp4"
			muxArgs = []string{"-movflags", "+use_metadata_tags+faststart"}
			outPath = withExt(path, ".mp4")
		default:
			outFmt = "matroska"
			outPath = withExt(path, ".mkv")
		}
		if outPath != path {
			if _, err := os.Stat(outPath); err == nil {
				t.skip(i, n, path, fmt.Sprintf("output %s already exists", outPath))
				return
			}
		}
	}

	plan := t.enc.PlanInfo(cfg.Mode, cfg.CQ, 0)
	srcSize := int64(0)
	if s, ok := info.Size(); ok {
		srcSize = s
	}
	if st, err := os.Stat(path); err == nil {
		srcSize = st.Size()
	}

	// track cleanup: drop audio/subtitle tracks outside the language
	// whitelist (the clean command's rules; sole tracks stay)
	var dropArgs []string
	var cleanupLines []string
	keptAudio := byType[probe.TypeAudio]
	keptSubs := len(byType[probe.TypeSubtitle])
	if len(t.whitelist) > 0 {
		tp := clean.PlanTracks(info, t.whitelist, false)
		for _, o := range tp.RemoveAudio {
			dropArgs = append(dropArgs, "-map", fmt.Sprintf("-0:a:%d", o))
		}
		for _, o := range tp.RemoveSubs {
			dropArgs = append(dropArgs, "-map", fmt.Sprintf("-0:s:%d", o))
		}
		dropped := map[int]bool{}
		for _, o := range tp.RemoveAudio {
			dropped[o] = true
		}
		var kept []probe.Typed
		for _, ta := range keptAudio {
			if !dropped[ta.Ordinal] {
				kept = append(kept, ta)
			}
		}
		keptAudio, keptSubs = kept, tp.KeptSubs
		cleanupLines = tp.Lines
		for _, s := range tp.SoleKept {
			t.rep.Notice(i, n, path, s)
		}
	}

	if cfg.DryRun {
		if cfg.Mode == encoders.ModeChromecast {
			t.rep.Would("Would create", i, n, outPath, fmt.Sprintf("source %s%s", media.HumanBytes(srcSize), scaleSuffix(scaleInfo)), cleanupLines...)
		} else {
			details := cleanupLines
			if outPath != path {
				details = append([]string{"output: " + outPath}, details...)
			}
			t.rep.Would("Would convert", i, n, path, fmt.Sprintf("%s, %s%s", media.HumanBytes(srcSize), plan, scaleSuffix(scaleInfo)), details...)
		}
		t.converted++
		return
	}

	// bitrate-capped modes target a fraction of the source video bitrate
	// per file; the plan line is computed once the bitrate is known
	srcKbps := sourceVideoKbps(real[0].Stream, info, srcSize)
	plan = t.enc.PlanInfo(cfg.Mode, cfg.CQ, srcKbps)

	details := []string{fmt.Sprintf("plan: %s%s", plan, scaleSuffix(scaleInfo))}
	details = append(details, cleanupLines...)
	if outPath != path {
		details = append(details, "output: "+outPath)
	}
	switch v.ColorTransfer {
	case "smpte2084":
		if tonemapped {
			details = append(details, "HDR10 source: tonemapping to SDR bt709 (bt2390)")
		} else {
			details = append(details, "HDR10 source: mastering display + light level metadata preserved")
		}
	case "arib-std-b67":
		if tonemapped {
			details = append(details, "HLG source: tonemapping to SDR bt709 (bt2390)")
		} else {
			details = append(details, "HLG source: color metadata preserved")
		}
	}
	if v.IsInterlaced() {
		details = append(details, fmt.Sprintf("interlaced source (%s): deinterlacing", v.FieldOrder))
	}
	srcDur := time.Duration(0)
	if d, ok := info.Duration(); ok {
		srcDur = time.Duration(d * float64(time.Second))
	}
	fileStart := time.Now()
	t.rep.Start("Processing", i, n, path, media.HumanBytes(srcSize), srcDur, details...)

	audioArgs, appended := t.audioPlan(info, keptAudio)

	// stream gate: verify the planned audio/subtitle/AC3 counts survived
	_, srcAc3 := ac3.CountTracks(info)
	gate := streamGate{ac3: srcAc3 + appended}
	switch {
	case len(t.whitelist) > 0:
		gate.check = true
		gate.audio = len(keptAudio) + appended
		gate.subs = keptSubs
	case appended > 0:
		gate.check = true
		gate.audio = len(byType[probe.TypeAudio]) + appended
		gate.subs = -1
	}

	attempts := t.attemptsFor(v, tonemapped, scaling)
	if len(attempts) == 0 {
		t.skip(i, n, path, fmt.Sprintf("tonemap requested but the %s backend cannot tonemap and ffmpeg has no libplacebo filter", t.enc.Label))
		return
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
	// interactive runs render their own progress from ffmpeg's stats and
	// drop the raw passthrough; verbose keeps the raw log visible
	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !cfg.Verbose {
		onStats = t.rep.Progress
	}
	for i, a := range attempts {
		t.rep.Attempt(t.enc.Label, a.label, i+1, len(attempts))
		args := t.encodeArgs(path, tmp, outFmt, muxArgs, cover, dropArgs, audioArgs, v, srcKbps, tonemapped, a)
		stderr, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, args...)
		if err == nil {
			lastStderr = stderr
			break
		}
		lastStderr = stderr
		os.Remove(tmp)
		// an ffmpeg killed by a signal means the run is being torn down:
		// stop everything, the TmpGuard has cleaned the temp file
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() < 0 {
			t.rep.Raw(ui.BadTone, "Interrupted, %s left untouched.\n", path)
			t.failures = append(t.failures, ui.Failure{Path: path, Msg: "interrupted"})
			t.failed++
			return
		}
		slog.Debug("encode attempt failed", "path", path, "attempt", i+1, "err", err)
		t.rep.AttemptFailed(a.label, i+1, len(attempts))
		if i == len(attempts)-1 {
			t.fail(path, fmt.Sprintf("failed to convert %s (left untouched)", path), run.LastErrLine(lastStderr))
			return
		}
	}

	// damaged sources decode with errors yet still exit 0; the captured
	// log tells how bad it was
	if n := countDecodeErrors(lastStderr); n > 0 {
		t.rep.Note(fmt.Sprintf("  warning: source has damaged video data (%d decode errors); output may glitch at those spots", n))
	}

	if cfg.Mode == encoders.ModeChromecast {
		if err := os.Rename(tmp, outPath); err != nil {
			t.fail(outPath, fmt.Sprintf("cannot finalize %s: %v", outPath, err), "")
			return
		}
		t.rep.Success("Created", outPath, 0, 0, "", time.Since(fileStart))
		t.converted++
		return
	}

	t.verifyAndSwap(i, n, path, outPath, tmp, info, srcSize, gate, time.Since(fileStart))
}

func scaleSuffix(scaleInfo string) string {
	if scaleInfo == "" {
		return ""
	}
	return ", " + scaleInfo
}

// attemptsFor builds the per-file retry ladder. Normal files use the
// three-step decode ladder (hw with subtitles, hw without, software —
// more tolerant of damaged streams). Tonemapped HDR files on NVENC swap
// in a GPU-resident CUDA chain; other backends (or a failed CUDA run)
// fall back to software tonemapping through libplacebo when the ffmpeg
// build has it.
func (t *tool) attemptsFor(v probe.Stream, tonemapped, scaling bool) []attempt {
	hwAuto := []string{"-hwaccel", "auto"}
	subsCopy := []string{"-c:s", "copy", "-c:d", "copy"}
	noSubs := []string{"-sn", "-dn"}

	if !tonemapped {
		chain := strings.Join(t.normalChain(v, scaling), ",")
		return []attempt{
			{decArgs: hwAuto, streamArgs: subsCopy, vf: chain, label: "hw decode, subtitles copied"},
			{decArgs: hwAuto, streamArgs: noSubs, vf: chain, label: "hw decode, subtitles dropped"},
			{decArgs: nil, streamArgs: noSubs, vf: chain, label: "software decode"},
		}
	}

	placebo := attempt{
		decArgs:    hwAuto,
		streamArgs: noSubs,
		vf:         strings.Join(t.placeboChain(v, scaling), ","),
		label:      "software decode, tonemap (libplacebo)",
	}
	if t.enc.Kind == encoders.NVENC {
		cudaChain := strings.Join(t.cudaChain(v, scaling), ",")
		attempts := []attempt{
			{decArgs: t.enc.CUDADecodeArgs(), streamArgs: subsCopy, vf: cudaChain, label: "cuda decode, tonemap, subtitles copied"},
			{decArgs: t.enc.CUDADecodeArgs(), streamArgs: noSubs, vf: cudaChain, label: "cuda decode, tonemap, subtitles dropped"},
		}
		if t.hasLibplacebo() {
			attempts = append(attempts, placebo)
		}
		return attempts
	}
	if !t.hasLibplacebo() {
		return nil
	}
	return []attempt{placebo}
}

// normalChain builds the system-memory filter chain: deinterlace first
// (scaling interlaced frames would smear them), then the optional
// downscale, then the encoder's format/upload fragments.
func (t *tool) normalChain(v probe.Stream, scaling bool) []string {
	var parts []string
	if v.IsInterlaced() {
		parts = append(parts, "yadif=mode=send_frame:parity=auto")
	}
	if scaling {
		parts = append(parts, fmt.Sprintf("scale=-2:%d:flags=lanczos", t.cfg.ScaleHeight))
	}
	parts = append(parts, t.enc.Filters(v.Is10Bit())...)
	return parts
}

// cudaChain builds the GPU-resident chain for tonemapping on NVENC:
// deinterlace and scale on the GPU, then the bt2390 tonemap to SDR bt709
// (heir of hevc-tablet.sh).
func (t *tool) cudaChain(v probe.Stream, scaling bool) []string {
	var parts []string
	if v.IsInterlaced() {
		parts = append(parts, "yadif_cuda=mode=send_frame:parity=auto")
	}
	if scaling {
		parts = append(parts, fmt.Sprintf("scale_cuda=%d:%d", evenWidth(v.Width, v.Height, t.cfg.ScaleHeight), t.cfg.ScaleHeight))
	}
	parts = append(parts, "tonemap_cuda=tonemap=bt2390:desat=0:primaries=bt709:transfer=bt709:matrix=bt709:format=nv12")
	return parts
}

// placeboChain tonemaps in software — it works with every encoder but
// needs the libplacebo filter in the ffmpeg build — and continues with
// the normal system-memory chain. The tonemapped frames are 8-bit SDR,
// so the upload always uses nv12.
func (t *tool) placeboChain(v probe.Stream, scaling bool) []string {
	var parts []string
	if v.IsInterlaced() {
		parts = append(parts, "yadif=mode=send_frame:parity=auto")
	}
	if scaling {
		parts = append(parts, fmt.Sprintf("scale=-2:%d:flags=lanczos", t.cfg.ScaleHeight))
	}
	parts = append(parts, placeboTonemap)
	parts = append(parts, t.enc.Filters(false)...)
	return parts
}

// evenWidth computes the aspect-preserving, even-rounded width for a
// target height (scale_cuda needs explicit dimensions).
func evenWidth(width, height, targetH int) int {
	w := width * targetH / height
	return w / 2 * 2
}

// hasLibplacebo reports (memoized) whether the ffmpeg build carries a
// working libplacebo tonemap chain, the software tonemapping fallback.
func (t *tool) hasLibplacebo() bool {
	if t.placebo == nil {
		ok := encoders.HasFilter(t.r, t.ffmpegBin, placeboTonemap)
		t.placebo = &ok
	}
	return *t.placebo
}

// audioPlan builds the audio encoding arguments for one file's kept audio
// streams and returns how many AC3 compatibility tracks get appended (for
// the stream-count gate). Per-stream output ordinals are positions among
// the kept tracks — input ordinals shift when the language cleanup drops
// tracks. Existing AAC tracks survive an aac target untouched; the ac3
// target appends via the ac3 command's planning rules for kept
// DTS/TrueHD/FLAC tracks.
func (t *tool) audioPlan(info *probe.Info, keptAudio []probe.Typed) (args []string, appended int) {
	switch t.audio.kind {
	case "copy":
		return []string{"-c:a", "copy"}, 0
	case "aac":
		for out, ta := range keptAudio {
			if ta.Stream.CodecName == "aac" {
				args = append(args, fmt.Sprintf("-c:a:%d", out), "copy")
				continue
			}
			args = append(args, fmt.Sprintf("-c:a:%d", out), "aac", fmt.Sprintf("-b:a:%d", out), t.audio.bitrate)
			if t.audio.channels > 0 {
				args = append(args, fmt.Sprintf("-ac:%d", out), strconv.Itoa(t.audio.channels))
			}
		}
		return args, 0
	default: // ac3
		args = []string{"-c:a", "copy"}
		keptOrdinals := map[int]bool{}
		for _, ta := range keptAudio {
			keptOrdinals[ta.Ordinal] = true
		}
		var plans []ac3.PlanEntry
		for _, p := range ac3.PlanCompat(info, ac3MinBR) {
			if keptOrdinals[p.Ordinal] {
				plans = append(plans, p)
			}
		}
		for i, p := range plans {
			out := len(keptAudio) + i
			args = append(args, "-map", fmt.Sprintf("0:a:%d", p.Ordinal),
				fmt.Sprintf("-c:a:%d", out), "ac3", fmt.Sprintf("-b:a:%d", out), t.audio.bitrate)
		}
		return args, len(plans)
	}
}

// encodeArgs assembles one ffmpeg invocation for the current attempt.
func (t *tool) encodeArgs(src, dst, outFmt string, muxArgs []string, cover []int, dropArgs, audioArgs []string, v probe.Stream, srcKbps int, tonemapped bool, a attempt) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y"}
	args = append(args, t.enc.InputArgs(t.cfg.VaapiDevice)...)
	if needsGenpts(src) {
		args = append(args, "-fflags", "+genpts")
	}
	args = append(args, a.decArgs...)
	args = append(args, "-i", src)
	args = append(args, "-map", "0", "-map_metadata", "0", "-map_chapters", "0")
	for _, o := range cover {
		args = append(args, "-map", fmt.Sprintf("-0:v:%d", o))
	}
	args = append(args, dropArgs...)
	if a.vf != "" {
		args = append(args, "-vf", a.vf)
	}
	args = append(args, t.enc.VideoArgs(t.cfg.Mode, t.cfg.CQ, srcKbps)...)
	if tonemapped {
		// the output is SDR; no HDR pixel format or color signaling may
		// leak onto it
		args = append(args, sdrColorArgs...)
	} else {
		args = append(args, t.enc.PixFmtArgs(v.Is10Bit())...)
		args = append(args, colorArgs(v)...)
	}
	args = append(args, audioArgs...)
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

// countStreamsOfType tallies the streams of one codec_type.
func countStreamsOfType(info *probe.Info, kind string) int {
	n := 0
	for _, s := range info.Streams {
		if s.CodecType == kind {
			n++
		}
	}
	return n
}

// sourceVideoKbps determines the source video bitrate: the video stream's
// own bitrate first, then size/duration as a whole-file estimate (the
// container bitrate is not consulted — it measures audio and subtitles
// too).
func sourceVideoKbps(v probe.Stream, info *probe.Info, srcSize int64) int {
	if br := v.BitRateInt(); br > 0 {
		return br / 1000
	}
	if dur, ok := info.Duration(); ok && dur > 0 && srcSize > 0 {
		// bash truncated the duration to integer seconds; keep the parity
		return int(float64(srcSize) * 8 / math.Trunc(dur) / 1000)
	}
	return 0
}

// verifyAndSwap applies the safety gates and swaps the file in place with a
// .old backup (or removes the original outright under -no-backup). outPath
// is the final name (it differs from path when the output container changes
// the extension); the backup keeps the source name.
func (t *tool) verifyAndSwap(i, n int, path, outPath, tmp string, info *probe.Info, srcSize int64, gate streamGate, took time.Duration) {
	srcDur, srcOK := info.Duration()
	tmpInfo, err := probe.Probe(t.r, t.ffprobeBin, tmp)
	if err != nil {
		t.fail(path, fmt.Sprintf("cannot probe the output: %v", err), "")
		return
	}
	newDur, newOK := tmpInfo.Duration()
	if srcOK && newOK && !gates.DurationKept(newDur, srcDur, 0.99) {
		t.fail(path, fmt.Sprintf("output (%.3fs) is much shorter than source (%.3fs) - damaged source, keeping original", newDur, srcDur), "")
		return
	}

	// track cleanup and appended AC3 tracks must survive the mux exactly
	// as planned (clean/ac3 command parity)
	if gate.check {
		newTotal, newAc3 := ac3.CountTracks(tmpInfo)
		newSubs := countStreamsOfType(tmpInfo, probe.TypeSubtitle)
		if newTotal != gate.audio || newAc3 != gate.ac3 ||
			(gate.subs >= 0 && newSubs != gate.subs) {
			t.fail(path, fmt.Sprintf("output streams do not match the plan (audio %d/%d, ac3 %d/%d, subs %d/%d) - keeping original",
				newTotal, gate.audio, newAc3, gate.ac3, newSubs, gate.subs), "")
			return
		}
	}

	st, err := os.Stat(tmp)
	if err != nil {
		t.fail(path, fmt.Sprintf("output is missing: %v", err), "")
		return
	}
	if !gates.Shrunk(st.Size(), srcSize) {
		// historical rendering: the path glues to the closing paren
		t.rep.SkipInline(i, n, path, fmt.Sprintf("re-encode is not smaller: %s -> %s", media.HumanBytes(srcSize), media.HumanBytes(st.Size())))
		t.skipped++
		return
	}

	// hevc-convert.sh parity: the replacement keeps the source's mtime
	// (read from the source before the swap; a -no-backup run has no .old)
	var srcMod time.Time
	if si, err := os.Stat(path); err == nil {
		srcMod = si.ModTime()
	}
	backupPath := path + ".old"
	var swapErr error
	if t.cfg.NoBackup {
		backupPath = ""
		swapErr = backup.ReplaceTo(path, tmp, outPath)
	} else {
		_, swapErr = backup.SwapTo(path, tmp, outPath)
	}
	if swapErr != nil {
		t.fail(path, swapErr.Error(), "")
		return
	}
	if !srcMod.IsZero() {
		os.Chtimes(outPath, srcMod, srcMod)
	}
	if t.cfg.NoBackup {
		t.rep.SuccessNoBackup("Converted", outPath, srcSize, st.Size(), took)
	} else {
		t.rep.Success("Converted", outPath, srcSize, st.Size(), backupPath, took)
	}
	t.bytesIn += srcSize
	t.bytesOut += st.Size()
	t.converted++
}
