// Package enhance AI-upscales video with Real-ESRGAN (ncnn Vulkan) and
// re-encodes to HEVC 1080p: NVDEC/hw frame extraction -> engine -> encoder
// registry encode. Port of the bash enhance.sh with the aspect-ratio
// fix (scale=-2:1080) and the engine downloaded natively (no wget/unzip).
package enhance

import (
	"archive/zip"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hranicka/mediatool/internal/cli"
	"github.com/hranicka/mediatool/internal/encoders"
	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/media"
	"github.com/hranicka/mediatool/internal/run"
	"github.com/hranicka/mediatool/internal/ui"
	"github.com/hranicka/mediatool/internal/walk"
)

const engineURL = "https://github.com/xinntao/Real-ESRGAN/releases/download/v0.2.5.0/realesrgan-ncnn-vulkan-20220424-ubuntu.zip"

const engineBinary = "realesrgan-ncnn-vulkan"

// outputSuffix marks produced files so they are skipped on the next run.
const outputSuffix = "_enhanced_1080p_hevc.mkv"

// Config holds the enhance subcommand settings.
type Config struct {
	Dir         string
	CQ          int
	EngineFlag  string
	VaapiDevice string
	Ignores     []string
	DryRun      bool
	Verbose     bool
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	enc        encoders.Encoder
	cfg        *Config
	enginePath string
	ignores    ignore.List
	rep        *ui.Reporter
	guard      *run.TmpGuard

	enhanced, skipped, failed int
	failures                  []ui.Failure
}

// fail renders the standard "Error: ..." line and records the failure for
// the summary.
func (t *tool) fail(path, msg string) {
	t.rep.Fail(msg, "")
	t.failures = append(t.failures, ui.Failure{Path: path, Msg: msg})
	t.failed++
}

// silentCapture captures a step's stderr without echoing it on
// interactive runs (the plain Stream passthrough stays for redirected
// output and -v).
func (t *tool) silentCapture() func(elapsed time.Duration, speed string) {
	if t.rep.Interactive() && !t.cfg.Verbose {
		return func(time.Duration, string) {}
	}
	return nil
}

// Run executes the enhance subcommand.
func Run(args []string) error {
	fs := flag.NewFlagSet("enhance", flag.ExitOnError)
	var cfg Config
	var verbose bool
	fs.IntVar(&cfg.CQ, "q", 22, "CQ quality value for the HEVC encode")
	fs.StringVar(&cfg.EngineFlag, "engine", "", "path to the realesrgan-ncnn-vulkan binary (default: $REALESRGAN_BIN, else auto-downloaded)")
	fs.StringVar(&cfg.VaapiDevice, "vaapi_device", "/dev/dri/renderD128", "VAAPI render device")
	fs.Var(cli.Ignores{Dst: &cfg.Ignores}, "i", "comma-separated substrings to ignore (also read from the dot ignore files)")
	fs.BoolVar(&cfg.DryRun, "n", false, "dry run: only list what would be enhanced")
	fs.BoolVar(&verbose, "v", false, "verbose/debug output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if verbose {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	cfg.Verbose = verbose
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mediatool enhance [flags] <directory>")
		return fmt.Errorf("enhance needs exactly one directory argument")
	}
	dir, err := cli.ResolveMediaTarget(fs.Arg(0), false)
	if err != nil {
		return err
	}
	cfg.Dir = dir

	ffmpegBin, ffprobeBin, err := run.Locate()
	if err != nil {
		return err
	}

	enginePath := cfg.EngineFlag
	if enginePath == "" {
		enginePath = os.Getenv("REALESRGAN_BIN")
	}
	if enginePath == "" {
		enginePath = filepath.Join(defaultEngineDir(), engineBinary)
	}

	ignores, warnings := ignore.Load(dir, "enhance", cfg.Ignores)
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
		enginePath: enginePath,
		ignores:    ignores,
		rep:        rep,
		guard:      run.NewTmpGuard(),
	}

	// the engine and the encoder are only needed for real runs
	if !cfg.DryRun {
		if err := ensureEngine(enginePath, rep); err != nil {
			return err
		}
		rep.Raw(ui.DimTone, "Testing encoder: %s...\n", encoders.Label(encoders.CodecHEVC, encoders.Candidates(encoders.CodecHEVC)[0]))
		enc, err := encoders.Probe(t.r, ffmpegBin, cfg.VaapiDevice, encoders.CodecHEVC, encoders.Candidates(encoders.CodecHEVC))
		if err != nil {
			return err
		}
		t.enc = enc
		rep.Raw(ui.Neutral, "Using encoder : %s (quality value: %d)\n", enc.Label, cfg.CQ)
	}
	return t.runAll()
}

// defaultEngineDir follows XDG data home conventions.
func defaultEngineDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "mediatool")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return engineBinary
	}
	return filepath.Join(home, ".local", "share", "mediatool")
}

// ensureEngine downloads and unpacks the standalone engine on first use.
func ensureEngine(enginePath string, rep *ui.Reporter) error {
	if _, err := os.Stat(enginePath); err == nil {
		return nil
	}
	dir := filepath.Dir(enginePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create engine dir: %w", err)
	}

	rep.Raw(ui.DimTone, "=========================================================\n")
	rep.Raw(ui.InfoTone, "Downloading native Real-ESRGAN engine...\n")
	rep.Raw(ui.DimTone, "=========================================================\n")
	zipPath := filepath.Join(dir, "engine.zip")
	if err := download(engineURL, zipPath, rep); err != nil {
		return fmt.Errorf("cannot download engine: %w", err)
	}
	if err := unpackEngine(zipPath, enginePath); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("cannot unpack engine: %w", err)
	}
	os.Remove(zipPath)
	rep.Raw(ui.GoodTone, "Download complete.\n")
	return nil
}

func download(url, dest string, rep *ui.Reporter) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http status %s", resp.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()
	var body io.Reader = resp.Body
	if resp.ContentLength > 0 {
		body = &progressReader{r: resp.Body, total: resp.ContentLength, rep: rep}
	}
	if _, err := io.Copy(f, body); err != nil {
		return err
	}
	return nil
}

// progressReader reports the download bytes to the reporter, throttled to
// whole percent steps.
type progressReader struct {
	r     io.Reader
	total int64
	done  int64
	rep   *ui.Reporter
	last  int
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	if pct := int(p.done * 100 / p.total); pct != p.last {
		p.last = pct
		p.rep.DownloadProgress("Real-ESRGAN engine", p.done, p.total)
	}
	return n, err
}

// unpackEngine extracts the binary and the models/ directory, guarding
// against zip-slip paths.
func unpackEngine(zipPath, enginePath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	modelsDir := filepath.Join(filepath.Dir(enginePath), "models")
	for _, f := range r.File {
		cleaned := filepath.Clean(f.Name)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) {
			continue
		}
		var dest string
		switch {
		case cleaned == engineBinary:
			dest = enginePath
		case strings.HasPrefix(cleaned, "models/"):
			dest = filepath.Join(modelsDir, strings.TrimPrefix(cleaned, "models/"))
		default:
			continue
		}
		if err := extractZipEntry(f, dest); err != nil {
			return err
		}
	}
	if _, err := os.Stat(enginePath); err != nil {
		return fmt.Errorf("archive does not contain %s", engineBinary)
	}
	return nil
}

func extractZipEntry(f *zip.File, dest string) error {
	if f.FileInfo().IsDir() {
		return os.MkdirAll(dest, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	src, err := f.Open()
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	defer dst.Close()
	_, err = io.Copy(dst, src)
	return err
}

func (t *tool) runAll() error {
	kvs := []ui.KV{
		ui.Pair("FFmpeg binary", t.ffmpegBin),
		ui.Pair("Engine", t.enginePath),
		ui.Pair("Target", t.cfg.Dir),
		ui.Pair("Ignore entries", strconv.Itoa(t.ignores.Len())),
	}
	if t.cfg.DryRun {
		kvs = append(kvs, ui.Pair("Extra", "DRY RUN"))
	}
	t.rep.Header(kvs)

	start := time.Now()
	files, err := walk.Media(t.cfg.Dir)
	if err != nil {
		return err
	}
	media := 0
	for i, path := range files {
		base := filepath.Base(path)
		if strings.HasSuffix(base, outputSuffix) {
			continue // output of a previous run
		}
		media++
		if t.ignores.Match(path) {
			t.rep.Skip(i+1, len(files), path, "ignored")
			continue
		}
		t.processFile(i+1, len(files), path)
	}
	t.rep.Found(media)

	t.rep.Summary("Batch processing complete!", "Enhanced", "skipped", ui.Outcome{
		Done:     t.enhanced,
		Skipped:  t.skipped,
		Failed:   t.failed,
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
	base := filepath.Base(path)
	if strings.HasSuffix(base, outputSuffix) {
		return // output of a previous run
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	outPath := filepath.Join(filepath.Dir(path), stem+outputSuffix)

	if _, err := os.Stat(outPath); err == nil {
		t.skip(i, n, outPath, "output already exists")
		return
	}

	srcSize := int64(0)
	if st, err := os.Stat(path); err == nil {
		srcSize = st.Size()
	}
	if t.cfg.DryRun {
		t.rep.Would("Would enhance", i, n, path, media.HumanBytes(srcSize))
		t.enhanced++
		return
	}

	t.rep.Raw(ui.DimTone, "=========================================================\n")
	t.rep.Start("Starting native HW accelerated process for", i, n, base, "", 0)
	t.rep.Raw(ui.DimTone, "=========================================================\n")

	dir := filepath.Dir(path)
	frameIn := filepath.Join(dir, "frames_in_"+stem)
	frameOut := filepath.Join(dir, "frames_out_"+stem)
	if err := os.MkdirAll(frameIn, 0o755); err != nil {
		t.fail(path, err.Error())
		return
	}
	t.guard.Add(frameIn, frameOut)
	finished := false
	defer func() {
		t.guard.Remove(frameIn, frameOut)
		if !finished {
			os.RemoveAll(frameIn)
			os.RemoveAll(frameOut)
		}
	}()

	// 1. hardware decoding into lossless frames
	t.rep.Raw(ui.InfoTone, "[1/3] Extracting frames using hardware decoding...\n")
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, t.silentCapture(),
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-hwaccel", "auto", "-i", path,
		"-qscale:v", "1", "-qmin", "1", "-qmax", "1", filepath.Join(frameIn, "frame_%08d.png")); err != nil {
		t.fail(path, "frame extraction failed. Skipping.")
		return
	}

	// 2. AI enhancement on the native GPU
	t.rep.Raw(ui.InfoTone, "[2/3] Running AI enhancement natively...\n")
	_, _ = run.StreamProgress(t.r, t.enginePath, t.silentCapture(),
		"-i", frameIn, "-o", frameOut,
		"-n", "realesr-animevideov3", "-s", "2", "-t", "256",
		"-m", filepath.Join(filepath.Dir(t.enginePath), "models"))
	if emptyDir(frameOut) {
		t.fail(path, "AI enhancement failed. Skipping.")
		return
	}

	// 3. stitching frames and encoding to HEVC; -2 keeps the aspect ratio
	t.rep.Raw(ui.InfoTone, "[3/3] Stitching frames and encoding to HEVC...\n")
	framerate := t.probeFramerate(path)
	if framerate == "" {
		t.fail(path, "cannot determine frame rate. Skipping.")
		return
	}
	encodeArgs := []string{"-hide_banner", "-loglevel", "error", "-stats", "-nostdin", "-y"}
	encodeArgs = append(encodeArgs, t.enc.InputArgs(t.cfg.VaapiDevice)...)
	encodeArgs = append(encodeArgs,
		"-hwaccel", "auto", "-framerate", framerate, "-i", filepath.Join(frameOut, "frame_%08d.png"), "-i", path,
		"-map", "0:v:0", "-map", "1:a?", "-map", "1:s?",
		"-vf", mergeFilters(t.enc))
	encodeArgs = append(encodeArgs, t.enc.VideoArgs(encoders.ModeMovie, t.cfg.CQ, 0)...)
	encodeArgs = append(encodeArgs, "-c:a", "copy", "-c:s", "copy", outPath)
	var onStats func(elapsed time.Duration, speed string)
	if t.rep.Interactive() && !t.cfg.Verbose {
		onStats = t.rep.Progress
	}
	if _, err := run.StreamProgress(t.r, t.ffmpegBin, onStats, encodeArgs...); err != nil {
		t.fail(path, "encoding failed. Skipping.")
		return
	}

	// 4. cleanup
	t.rep.Raw(ui.DimTone, "Cleaning up temporary frame directories...\n")
	os.RemoveAll(frameIn)
	os.RemoveAll(frameOut)
	finished = true

	t.rep.Raw(ui.GoodTone, "Finished processing: %s\n", outPath)
	t.enhanced++
}

// mergeFilters builds the encode-side filter chain: aspect-preserving
// downscale to 1080p plus the encoder's required fragments (VAAPI upload).
func mergeFilters(enc encoders.Encoder) string {
	parts := []string{"scale=-2:1080:flags=lanczos"}
	parts = append(parts, enc.Filters(false)...)
	return strings.Join(parts, ",")
}

func (t *tool) probeFramerate(path string) string {
	out, err := t.r.Output(t.ffprobeBin, "-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=r_frame_rate", "-of", "default=noprint_wrappers=1:nokey=1", path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func emptyDir(path string) bool {
	entries, err := os.ReadDir(path)
	return err != nil || len(entries) == 0
}
