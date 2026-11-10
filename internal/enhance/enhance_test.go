package enhance

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/media-tool/internal/encoders"
	"github.com/hranicka/media-tool/internal/ignore"
	"github.com/hranicka/media-tool/internal/run"
)

// fakeFF fakes ffmpeg (extraction no-op, encode writes the output),
// ffprobe (raw r_frame_rate text) and the engine (writes one frame).
type fakeFF struct {
	ffmpegBin  string
	enginePath string
	encodeLog  [][]string
	engineLog  [][]string
	streamHook func(name string, args []string) ([]byte, error)
}

func (f *fakeFF) Output(name string, args ...string) ([]byte, error) {
	if name == "ffprobe" {
		return []byte("30000/1001\n"), nil
	}
	return []byte{}, nil // encoder probe
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	if f.streamHook != nil {
		return f.streamHook(name, args)
	}
	if name != f.ffmpegBin {
		// engine invocation: create a frame in the -o directory
		f.engineLog = append(f.engineLog, args)
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				if err := os.MkdirAll(args[i+1], 0o755); err != nil {
					return nil, err
				}
				return nil, os.WriteFile(filepath.Join(args[i+1], "frame_00000001.png"), []byte("x"), 0o644)
			}
		}
		return nil, nil
	}
	for _, a := range args {
		if a == "-framerate" {
			// encode call: last arg is the output file
			f.encodeLog = append(f.encodeLog, args)
			return nil, os.WriteFile(args[len(args)-1], []byte("x"), 0o644)
		}
	}
	// extraction call
	return nil, nil
}

func setup(t *testing.T) (string, *fakeFF) {
	t.Helper()
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine-stub")
	if err := os.WriteFile(engine, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir, &fakeFF{ffmpegBin: "ffmpeg", enginePath: engine}
}

func newTestTool(t *testing.T, dir string, ff run.Runner, engine string, cfg *Config, out *bytes.Buffer) *tool {
	t.Helper()
	return &tool{
		r:          ff,
		ffmpegBin:  "ffmpeg",
		ffprobeBin: "ffprobe",
		enc:        encoders.Encoder{Kind: encoders.NVENC, Label: "Nvidia NVENC"},
		cfg:        cfg,
		enginePath: engine,
		ignores:    ignore.Load(dir, "enhance", cfg.Ignores),
		out:        out,
		guard:      &run.TmpGuard{},
	}
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hasPair(args []string, a, b string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return true
		}
	}
	return false
}

func TestEnhanceHappyPath(t *testing.T) {
	dir, ff := setup(t)
	src := filepath.Join(dir, "anime.mkv")
	writeFile(t, src, 1_000_000)

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, ff.enginePath, &Config{Dir: dir, CQ: 22}, out)
	tl.processFile(src)

	outPath := filepath.Join(dir, "anime_enhanced_1080p_hevc.mkv")
	if tl.enhanced != 1 || tl.failed != 0 {
		t.Fatalf("counters: %+v out=%s", tl, out.String())
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output missing: %v", err)
	}

	// engine invoked with the anime model, 2x, safe tile size
	if len(ff.engineLog) != 1 {
		t.Fatalf("engine not invoked: %v", ff.engineLog)
	}
	engineArgs := ff.engineLog[0]
	for _, want := range []string{"-n", "realesr-animevideov3", "-s", "2", "-t", "256"} {
		found := false
		for _, a := range engineArgs {
			if a == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("engine args missing %q: %v", want, engineArgs)
		}
	}

	args := ff.encodeLog[0]
	if !hasPair(args, "-vf", "scale=-2:1080:flags=lanczos") {
		t.Errorf("aspect-preserving scale missing: %v", args)
	}
	if !hasPair(args, "-framerate", "30000/1001") || !hasPair(args, "-c:a", "copy") {
		t.Errorf("encode args wrong: %v", args)
	}
	if !hasPair(args, "-c:v", "hevc_nvenc") {
		t.Errorf("registry encoder missing: %v", args)
	}

	// frame directories must be cleaned up
	if _, err := os.Stat(filepath.Join(dir, "frames_in_anime")); !os.IsNotExist(err) {
		t.Error("frames_in must be cleaned up")
	}
	if _, err := os.Stat(filepath.Join(dir, "frames_out_anime")); !os.IsNotExist(err) {
		t.Error("frames_out must be cleaned up")
	}
}

func TestEnhanceSkipExistingOutput(t *testing.T) {
	dir, ff := setup(t)
	src := filepath.Join(dir, "anime.mkv")
	writeFile(t, src, 1000)
	writeFile(t, filepath.Join(dir, "anime_enhanced_1080p_hevc.mkv"), 10)

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, ff.enginePath, &Config{Dir: dir}, out)
	tl.processFile(src)

	if tl.skipped != 1 || len(ff.encodeLog) != 0 {
		t.Errorf("existing output must skip: %+v out=%s", tl, out.String())
	}
}

func TestEnhanceSkipsOwnOutputs(t *testing.T) {
	dir, ff := setup(t)
	prev := filepath.Join(dir, "old_enhanced_1080p_hevc.mkv")
	writeFile(t, prev, 1000)

	tl := newTestTool(t, dir, ff, ff.enginePath, &Config{Dir: dir}, &bytes.Buffer{})
	tl.processFile(prev)

	if tl.enhanced != 0 || tl.skipped != 0 || tl.failed != 0 {
		t.Errorf("own outputs must not be processed at all: %+v", tl)
	}
}

func TestEnhanceDryRun(t *testing.T) {
	dir, _ := setup(t)
	src := filepath.Join(dir, "anime.mkv")
	writeFile(t, src, 1_000_000)

	// no engine and no encoder needed in dry-run mode
	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, &fakeFF{ffmpegBin: "ffmpeg", enginePath: filepath.Join(dir, "missing-engine")},
		filepath.Join(dir, "missing-engine"), &Config{Dir: dir, DryRun: true}, out)
	tl.processFile(src)

	if tl.enhanced != 1 || !strings.Contains(out.String(), "Would enhance") {
		t.Errorf("dry run must list without engine: %+v out=%s", tl, out.String())
	}
}

func TestEnhanceEngineFailure(t *testing.T) {
	dir, ff := setup(t)
	src := filepath.Join(dir, "anime.mkv")
	writeFile(t, src, 1000)

	// the engine silently produces no frames -> failure, dirs cleaned up
	ff.streamHook = func(name string, args []string) ([]byte, error) {
		if name != ff.ffmpegBin {
			return nil, nil
		}
		for _, a := range args {
			if a == "-framerate" {
				return nil, os.WriteFile(args[len(args)-1], []byte("x"), 0o644)
			}
		}
		return nil, nil
	}

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, ff.enginePath, &Config{Dir: dir}, out)
	tl.processFile(src)

	if tl.failed != 1 || !strings.Contains(out.String(), "AI enhancement failed") {
		t.Errorf("engine failure must be reported: %+v out=%s", tl, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "frames_in_anime")); !os.IsNotExist(err) {
		t.Error("frame dirs must be cleaned up after failure")
	}
}

func TestEnhanceRunAllWithIgnores(t *testing.T) {
	dir, ff := setup(t)
	a := filepath.Join(dir, "a.mkv")
	b := filepath.Join(dir, "b.mkv")
	writeFile(t, a, 1000)
	writeFile(t, b, 1000)
	if err := os.WriteFile(filepath.Join(dir, ".enhance-ignore"), []byte("b.mkv\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := &bytes.Buffer{}
	tl := newTestTool(t, dir, ff, ff.enginePath, &Config{Dir: dir}, out)
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if len(ff.encodeLog) != 1 {
		t.Fatalf("only a.mkv must be enhanced: %v", ff.encodeLog)
	}
	if !strings.Contains(out.String(), "Skipping (ignored)") {
		t.Errorf("ignore skip line missing: %s", out.String())
	}
}

func TestMergeFilters(t *testing.T) {
	nvenc := encoders.Encoder{Kind: encoders.NVENC}
	if got := mergeFilters(nvenc); got != "scale=-2:1080:flags=lanczos" {
		t.Errorf("nvenc filters: %q", got)
	}
	vaapi := encoders.Encoder{Kind: encoders.VAAPI}
	if got := mergeFilters(vaapi); got != "scale=-2:1080:flags=lanczos,format=nv12,hwupload" {
		t.Errorf("vaapi filters: %q", got)
	}
}

func TestEnsureEngineSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	engine := filepath.Join(dir, "engine")
	if err := os.WriteFile(engine, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureEngine(engine, &bytes.Buffer{}); err != nil {
		t.Errorf("existing engine must be accepted: %v", err)
	}
}
