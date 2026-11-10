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
	"strings"

	"github.com/hranicka/media-tool/internal/cli"
	"github.com/hranicka/media-tool/internal/encoders"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/media"
	"github.com/hranicka/media-tool/internal/run"
	"github.com/hranicka/media-tool/internal/walk"
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
}

type tool struct {
	r          run.Runner
	ffmpegBin  string
	ffprobeBin string
	enc        encoders.Encoder
	cfg        *Config
	enginePath string
	ignores    ignore.List
	out        io.Writer
	guard      *run.TmpGuard

	enhanced, skipped, failed int
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
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: media-tool enhance [flags] <directory>")
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

	t := &tool{
		r:          run.Cmd{},
		ffmpegBin:  ffmpegBin,
		ffprobeBin: ffprobeBin,
		cfg:        &cfg,
		enginePath: enginePath,
		ignores:    ignore.Load(dir, "enhance", cfg.Ignores),
		out:        os.Stdout,
		guard:      run.NewTmpGuard(),
	}

	// the engine and the encoder are only needed for real runs
	if !cfg.DryRun {
		if err := ensureEngine(enginePath, t.out); err != nil {
			return err
		}
		fmt.Fprintf(t.out, "Testing encoder: %s...\n", encoders.Candidates()[0].Label())
		enc, err := encoders.Probe(t.r, ffmpegBin, cfg.VaapiDevice, encoders.Candidates())
		if err != nil {
			return err
		}
		t.enc = enc
		fmt.Fprintf(t.out, "Using encoder : %s (quality value: %d)\n", enc.Label, cfg.CQ)
	}
	return t.runAll()
}

// defaultEngineDir follows XDG data home conventions.
func defaultEngineDir() string {
	if x := os.Getenv("XDG_DATA_HOME"); x != "" {
		return filepath.Join(x, "media-tool")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return engineBinary
	}
	return filepath.Join(home, ".local", "share", "media-tool")
}

// ensureEngine downloads and unpacks the standalone engine on first use.
func ensureEngine(enginePath string, w io.Writer) error {
	if _, err := os.Stat(enginePath); err == nil {
		return nil
	}
	dir := filepath.Dir(enginePath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create engine dir: %w", err)
	}

	fmt.Fprintln(w, "=========================================================")
	fmt.Fprintln(w, "Downloading native Real-ESRGAN engine...")
	fmt.Fprintln(w, "=========================================================")
	zipPath := filepath.Join(dir, "engine.zip")
	if err := download(engineURL, zipPath); err != nil {
		return fmt.Errorf("cannot download engine: %w", err)
	}
	if err := unpackEngine(zipPath, enginePath); err != nil {
		os.Remove(zipPath)
		return fmt.Errorf("cannot unpack engine: %w", err)
	}
	os.Remove(zipPath)
	fmt.Fprintln(w, "Download complete.")
	return nil
}

func download(url, dest string) error {
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
	if _, err := io.Copy(f, resp.Body); err != nil {
		return err
	}
	return nil
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
	fmt.Fprintf(t.out, "FFmpeg binary : %s\n", t.ffmpegBin)
	fmt.Fprintf(t.out, "Engine        : %s\n", t.enginePath)
	fmt.Fprintf(t.out, "Target        : %s\n", t.cfg.Dir)
	fmt.Fprintf(t.out, "Ignore entries: %d\n", t.ignores.Len())
	if t.cfg.DryRun {
		fmt.Fprintf(t.out, "Extra         : DRY RUN\n")
	}
	fmt.Fprintln(t.out, "==========================================")

	files, err := walk.Media(t.cfg.Dir)
	if err != nil {
		return err
	}
	for _, path := range files {
		base := filepath.Base(path)
		if strings.HasSuffix(base, outputSuffix) {
			continue // output of a previous run
		}
		if t.ignores.Match(path) {
			fmt.Fprintf(t.out, "Skipping (ignored): %s\n", path)
			continue
		}
		t.processFile(path)
	}

	fmt.Fprintln(t.out, "==========================================")
	fmt.Fprintf(t.out, "Batch processing complete! Enhanced: %d, skipped: %d, failed: %d.\n", t.enhanced, t.skipped, t.failed)
	if t.failed > 0 {
		return fmt.Errorf("%d file(s) failed", t.failed)
	}
	return nil
}

func (t *tool) skip(reason string, path string) {
	fmt.Fprintf(t.out, "Skipping (%s): %s\n", reason, path)
	t.skipped++
}

func (t *tool) processFile(path string) {
	base := filepath.Base(path)
	if strings.HasSuffix(base, outputSuffix) {
		return // output of a previous run
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	outPath := filepath.Join(filepath.Dir(path), stem+outputSuffix)

	if _, err := os.Stat(outPath); err == nil {
		t.skip("output already exists", outPath)
		return
	}

	srcSize := int64(0)
	if st, err := os.Stat(path); err == nil {
		srcSize = st.Size()
	}
	if t.cfg.DryRun {
		fmt.Fprintf(t.out, "Would enhance: %s (%s)\n", path, media.HumanBytes(srcSize))
		t.enhanced++
		return
	}

	fmt.Fprintln(t.out, "=========================================================")
	fmt.Fprintf(t.out, "Starting native HW accelerated process for: %s\n", base)
	fmt.Fprintln(t.out, "=========================================================")

	dir := filepath.Dir(path)
	frameIn := filepath.Join(dir, "frames_in_"+stem)
	frameOut := filepath.Join(dir, "frames_out_"+stem)
	if err := os.MkdirAll(frameIn, 0o755); err != nil {
		fmt.Fprintf(t.out, "Error: %v\n", err)
		t.failed++
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
	fmt.Fprintln(t.out, "[1/3] Extracting frames using hardware decoding...")
	if _, err := t.r.Stream(t.ffmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-hwaccel", "auto", "-i", path,
		"-qscale:v", "1", "-qmin", "1", "-qmax", "1", filepath.Join(frameIn, "frame_%08d.png")); err != nil {
		fmt.Fprintf(t.out, "Error: frame extraction failed. Skipping.\n")
		t.failed++
		return
	}

	// 2. AI enhancement on the native GPU
	fmt.Fprintln(t.out, "[2/3] Running AI enhancement natively...")
	_, _ = t.r.Stream(t.enginePath,
		"-i", frameIn, "-o", frameOut,
		"-n", "realesr-animevideov3", "-s", "2", "-t", "256",
		"-m", filepath.Join(filepath.Dir(t.enginePath), "models"))
	if emptyDir(frameOut) {
		fmt.Fprintf(t.out, "Error: AI enhancement failed. Skipping.\n")
		t.failed++
		return
	}

	// 3. stitching frames and encoding to HEVC; -2 keeps the aspect ratio
	fmt.Fprintln(t.out, "[3/3] Stitching frames and encoding to HEVC...")
	framerate := t.probeFramerate(path)
	if framerate == "" {
		fmt.Fprintf(t.out, "Error: cannot determine frame rate. Skipping.\n")
		t.failed++
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
	if _, err := t.r.Stream(t.ffmpegBin, encodeArgs...); err != nil {
		fmt.Fprintf(t.out, "Error: encoding failed. Skipping.\n")
		t.failed++
		return
	}

	// 4. cleanup
	fmt.Fprintln(t.out, "Cleaning up temporary frame directories...")
	os.RemoveAll(frameIn)
	os.RemoveAll(frameOut)
	finished = true

	fmt.Fprintf(t.out, "Finished processing: %s\n", outPath)
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
