package align

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/probe"
	"github.com/hranicka/mediatool/internal/vhash"
)

const batchRefJSON = `{"streams":[` +
	`{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
	`{"index":1,"codec_name":"ac3","codec_type":"audio","channels":6,"tags":{"language":"eng"}},` +
	`{"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"eng"}}],` +
	`"format":{"duration":"20.0","size":"1000","bit_rate":"1"}}`

const batchDubJSON = `{"streams":[` +
	`{"index":0,"codec_name":"mpeg2video","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
	`{"index":1,"codec_name":"mp2","codec_type":"audio","channels":2,"tags":{"language":"ces"}}],` +
	`"format":{"duration":"18.0","size":"1000","bit_rate":"1"}}`

const batchOutJSON = `{"streams":[` +
	`{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576,"disposition":{"attached_pic":0}},` +
	`{"index":1,"codec_name":"ac3","codec_type":"audio","channels":6,"tags":{"language":"eng"}},` +
	`{"index":2,"codec_name":"subrip","codec_type":"subtitle","tags":{"language":"eng"}},` +
	`{"index":3,"codec_name":"aac","codec_type":"audio","channels":2,"tags":{"language":"ces","title":"Dubbed (resampled)"}}],` +
	`"format":{"duration":"20.0","size":"2000","bit_rate":"1"}}`

func frameSeeds(start, count int) []byte {
	buf := make([]byte, 0, count*vhash.FrameLen)
	for t := start; t < start+count; t++ {
		var h [vhash.FrameLen]byte
		rng := rand.New(rand.NewSource(int64(t)))
		rng.Read(h[:])
		buf = append(buf, h[:]...)
	}
	return buf
}

func batchFixture(t *testing.T) (dir, en, cz string, ff *fakeFF) {
	t.Helper()
	dir = t.TempDir()
	en = filepath.Join(dir, "en")
	cz = filepath.Join(dir, "cz")
	if err := os.MkdirAll(en, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cz, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(en, "Show.S01E01.mkv"), 1000)
	writeFile(t, filepath.Join(en, "Show.S01E02.mkv"), 1000)
	writeFile(t, filepath.Join(cz, "Show.1x01.CZ.mkv"), 1000)

	ff = &fakeFF{
		probeFor: func(src string) (string, bool) {
			switch {
			case strings.HasSuffix(src, ".tmp"):
				return batchOutJSON, true
			case filepath.Base(src) == "Show.S01E01.mkv", filepath.Base(src) == "Show.S01E02.mkv":
				return batchRefJSON, true
			case filepath.Base(src) == "Show.1x01.CZ.mkv":
				return batchDubJSON, true
			}
			return "", false
		},
		framesFor: func(src string) []byte {
			if strings.Contains(src, "CZ") {
				return frameSeeds(10, 90)
			}
			return frameSeeds(0, 100)
		},
		rawFor: func(src string) []byte {
			if strings.HasSuffix(src, "dub.raw") {
				return dubRaw(18 * sampleRate)
			}
			return []byte("muxed output")
		},
	}
	return dir, en, cz, ff
}

func batchTool(t *testing.T, ff *fakeFF, en, cz string, cfg *Config, out *bytes.Buffer) *tool {
	t.Helper()
	cfg.RefDir, cfg.DubDir = en, cz
	cfg.Interval, cfg.MatchDist, cfg.Bitrate = 0.2, 45, "192k"
	cfg.LangRef, cfg.LangDub = "eng", "ces"
	tl := newTestTool(t, "", ff, cfg, out)
	return tl
}

func TestBatchAlignInPlace(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	out := &bytes.Buffer{}
	cfg := &Config{}
	tl := batchTool(t, ff, en, cz, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}

	got := out.String()
	if !strings.Contains(got, "pair: Show.S01E01.mkv <- Show.1x01.CZ.mkv") {
		t.Errorf("missing pairing line: %s", got)
	}
	if !strings.Contains(got, "skip: Show.S01E02.mkv - no counterpart") {
		t.Errorf("missing counterpart notice: %s", got)
	}
	if !strings.Contains(got, "Aligned: 1, skipped: 1, failed: 0.") {
		t.Errorf("unexpected summary: %s", got)
	}
	ref := filepath.Join(en, "Show.S01E01.mkv")
	if data, err := os.ReadFile(ref); err != nil || string(data) != "muxed output" {
		t.Errorf("aligned reference missing or wrong: %q, %v", data, err)
	}
	for _, backup := range []string{ref + ".old", filepath.Join(cz, "Show.1x01.CZ.mkv") + ".old"} {
		if _, err := os.Stat(backup); err != nil {
			t.Errorf("missing backup %s: %v", backup, err)
		}
	}
	if _, err := os.Stat(filepath.Join(cz, "Show.1x01.CZ.mkv")); err == nil {
		t.Error("the dub must be moved to .old")
	}
	if data, err := os.ReadFile(filepath.Join(en, "Show.S01E02.mkv")); err != nil || len(data) != 1000 {
		t.Errorf("the unpaired reference must stay untouched: %v", err)
	}
	if len(ff.streamLog) != 2 {
		t.Errorf("expected dub extraction + mux, got %d stream calls", len(ff.streamLog))
	}
}

func TestBatchAlignNoBackup(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	out := &bytes.Buffer{}
	cfg := &Config{NoBackup: true}
	tl := batchTool(t, ff, en, cz, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}
	ref := filepath.Join(en, "Show.S01E01.mkv")
	if _, err := os.Stat(ref + ".old"); err == nil {
		t.Error("-no-backup must not create a reference backup")
	}
	dub := filepath.Join(cz, "Show.1x01.CZ.mkv")
	if _, err := os.Stat(dub); err == nil {
		t.Error("-no-backup must remove the dub")
	}
	if _, err := os.Stat(dub + ".old"); err == nil {
		t.Error("-no-backup must not create a dub backup")
	}
	if data, err := os.ReadFile(ref); err != nil || string(data) != "muxed output" {
		t.Errorf("aligned reference missing: %q, %v", data, err)
	}
	if !strings.Contains(out.String(), "no backup") {
		t.Errorf("missing no-backup line: %s", out.String())
	}
}

func TestBatchAlignDryRun(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	out := &bytes.Buffer{}
	cfg := &Config{DryRun: true}
	tl := batchTool(t, ff, en, cz, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "Would align") {
		t.Errorf("missing dry-run plan: %s", got)
	}
	if !strings.Contains(got, "DRY RUN") {
		t.Errorf("missing dry-run header: %s", got)
	}
	ref := filepath.Join(en, "Show.S01E01.mkv")
	if data, err := os.ReadFile(ref); err != nil || len(data) != 1000 {
		t.Errorf("dry run must not touch the reference: %v", err)
	}
	if _, err := os.Stat(ref + ".old"); err == nil {
		t.Error("dry run must not create backups")
	}
	if len(ff.streamLog) != 0 {
		t.Errorf("dry run must not mux: %v", ff.streamLog)
	}
}

func TestBatchAlignPairOnly(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	out := &bytes.Buffer{}
	cfg := &Config{PairOnly: true}
	tl := batchTool(t, ff, en, cz, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "pair: Show.S01E01.mkv") || !strings.Contains(got, "Paired: 1") {
		t.Errorf("unexpected pair-only output: %s", got)
	}
	if len(ff.streamLog) != 0 {
		t.Errorf("pair-only must not decode: %v", ff.streamLog)
	}
}

func TestBatchAlignBackupCollision(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	ref := filepath.Join(en, "Show.S01E01.mkv")
	if err := os.WriteFile(ref+".old", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	tl := batchTool(t, ff, en, cz, &Config{}, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v", err)
	}
	if !strings.Contains(out.String(), "backup "+ref+".old already exists") {
		t.Errorf("missing backup collision skip: %s", out.String())
	}
	if len(ff.streamLog) != 0 {
		t.Error("backup collision must be detected before decoding")
	}
}

func TestBatchAlignAlreadyAligned(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	ff.probeFor = func(src string) (string, bool) {
		switch {
		case strings.HasSuffix(src, ".tmp"):
			return batchOutJSON, true
		case filepath.Base(src) == "Show.S01E01.mkv":
			return `{"streams":[` +
				`{"index":0,"codec_name":"h264","codec_type":"video","width":720,"height":576},` +
				`{"index":1,"codec_name":"ac3","codec_type":"audio","tags":{"language":"eng"}},` +
				`{"index":2,"codec_name":"aac","codec_type":"audio","tags":{"language":"ces","title":"Dubbed (resampled)"}}],` +
				`"format":{"duration":"20.0","size":"1000","bit_rate":"1"}}`, true
		case filepath.Base(src) == "Show.S01E02.mkv":
			return batchRefJSON, true
		default:
			return batchDubJSON, true
		}
	}
	out := &bytes.Buffer{}
	tl := batchTool(t, ff, en, cz, &Config{}, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v", err)
	}
	if !strings.Contains(out.String(), "already has a dubbed track") {
		t.Errorf("missing already-aligned skip: %s", out.String())
	}
	if len(ff.streamLog) != 0 {
		t.Error("already-aligned files must not be decoded")
	}
}

func TestOutputTarget(t *testing.T) {
	audioVideo := &probe.Info{Streams: []probe.Stream{
		{CodecType: probe.TypeVideo, CodecName: "h264"},
		{CodecType: probe.TypeAudio, CodecName: "aac"},
	}}
	withSub := &probe.Info{Streams: []probe.Stream{
		{CodecType: probe.TypeVideo, CodecName: "h264"},
		{CodecType: probe.TypeAudio, CodecName: "aac"},
		{CodecType: probe.TypeSubtitle, CodecName: "subrip"},
	}}
	for _, tt := range []struct {
		ref, wantPath, wantFmt string
		info                   *probe.Info
	}{
		{"/lib/movie.mkv", "/lib/movie.mkv", "matroska", withSub},
		{"/lib/movie.mp4", "/lib/movie.mp4", "mp4", audioVideo},
		{"/lib/movie.mp4", "/lib/movie.mkv", "matroska", withSub},
		{"/lib/movie.ts", "/lib/movie.mkv", "matroska", audioVideo},
	} {
		gotPath, gotFmt := outputTarget(filepath.FromSlash(tt.ref), tt.info)
		if gotPath != filepath.FromSlash(tt.wantPath) || gotFmt != tt.wantFmt {
			t.Errorf("outputTarget(%s) = %s/%s, want %s/%s", tt.ref, gotPath, gotFmt, tt.wantPath, tt.wantFmt)
		}
	}
}

func TestHasDubbedTrack(t *testing.T) {
	info := &probe.Info{Streams: []probe.Stream{{
		CodecType: probe.TypeAudio,
		Tags:      probe.Tags{HandlerName: "Dubbed (resampled)"},
	}}}
	if !hasDubbedTrack(info) {
		t.Error("handler_name marker must count as an existing dubbed track (MP4)")
	}
}

func countCacheEntries(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".cache") {
			n++
		}
	}
	return n
}

func TestBatchAlignFrameCache(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	out := &bytes.Buffer{}
	cfg := &Config{DryRun: true, CacheDir: cacheDir}

	first := batchTool(t, ff, en, cz, cfg, out)
	first.cache = vhash.Cache{Dir: cacheDir}
	defer first.guard.Cleanup()
	if err := first.runBatch(); err != nil {
		t.Fatalf("first dry run: %v (out: %s)", err, out.String())
	}
	if ff.frameCalls != 2 {
		t.Fatalf("first dry run: got %d frame extractions, want 2 (reference + dub)", ff.frameCalls)
	}
	if n := countCacheEntries(t, cacheDir); n != 2 {
		t.Fatalf("first dry run: cache has %d entries, want 2", n)
	}

	// the second dry run must reuse both entries and decode nothing
	ff.frameCalls = 0
	out.Reset()
	second := batchTool(t, ff, en, cz, cfg, out)
	second.cache = vhash.Cache{Dir: cacheDir}
	defer second.guard.Cleanup()
	if err := second.runBatch(); err != nil {
		t.Fatalf("second dry run: %v", err)
	}
	if ff.frameCalls != 0 {
		t.Errorf("second dry run re-extracted %d file(s), want cache hits", ff.frameCalls)
	}

	// a real -no-backup run reuses the cache and removes both entries
	cfg.DryRun = false
	cfg.NoBackup = true
	ff.frameCalls = 0
	out.Reset()
	real := batchTool(t, ff, en, cz, cfg, out)
	real.cache = vhash.Cache{Dir: cacheDir}
	defer real.guard.Cleanup()
	if err := real.runBatch(); err != nil {
		t.Fatalf("real run: %v (out: %s)", err, out.String())
	}
	if ff.frameCalls != 0 {
		t.Errorf("real run re-extracted %d file(s), want cache hits", ff.frameCalls)
	}
	if n := countCacheEntries(t, cacheDir); n != 0 {
		t.Errorf("-no-backup must remove the cache entries, still have %d", n)
	}
}

func TestCleanCache(t *testing.T) {
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "cache")
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	cache := vhash.Cache{Dir: cacheDir}
	if err := cache.Store(media, 0.2, testHashes(5)); err != nil {
		t.Fatal(err)
	}
	if n := countCacheEntries(t, cacheDir); n != 1 {
		t.Fatalf("setup: cache has %d entries, want 1", n)
	}

	out := &bytes.Buffer{}
	cfg := &Config{CacheDir: cacheDir, DryRun: true}
	tl := newTestTool(t, dir, &fakeFF{}, cfg, out)
	if err := tl.cleanCache(); err != nil {
		t.Fatalf("dry cleanCache: %v", err)
	}
	if !strings.Contains(out.String(), "Would remove") || countCacheEntries(t, cacheDir) != 1 {
		t.Errorf("dry run must report but keep the cache: %s", out.String())
	}

	out.Reset()
	cfg.DryRun = false
	tl = newTestTool(t, dir, &fakeFF{}, cfg, out)
	if err := tl.cleanCache(); err != nil {
		t.Fatalf("cleanCache: %v", err)
	}
	if !strings.Contains(out.String(), "Removed: 1") {
		t.Errorf("missing removal summary: %s", out.String())
	}
	if n := countCacheEntries(t, cacheDir); n != 0 {
		t.Errorf("cache still has %d entries", n)
	}
}

// testHashes builds deterministic signatures for the cache tests.
func testHashes(n int) [][vhash.HashLen]byte {
	out := make([][vhash.HashLen]byte, n)
	for i := range out {
		out[i][0] = byte(i)
	}
	return out
}

func TestBatchAlignNoCache(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	cacheDir := filepath.Join(t.TempDir(), "cache")
	out := &bytes.Buffer{}
	cfg := &Config{DryRun: true, CacheDir: cacheDir, NoCache: true}
	tl := batchTool(t, ff, en, cz, cfg, out)
	tl.cache = vhash.Cache{Dir: cacheDir}
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v", err)
	}
	if ff.frameCalls != 2 {
		t.Errorf("got %d frame extractions, want 2 (cache disabled)", ff.frameCalls)
	}
	if n := countCacheEntries(t, cacheDir); n != 0 {
		t.Errorf("-no-cache must not write entries, found %d", n)
	}
}

func TestMixedFileAndDir(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	ref := filepath.Join(en, "Show.S01E01.mkv")
	cfg := &Config{Ref: ref, DubDir: cz, Mixed: true}
	cfg.Interval, cfg.MatchDist, cfg.Bitrate = 0.2, 45, "192k"
	cfg.LangRef, cfg.LangDub = "eng", "ces"
	out := &bytes.Buffer{}
	tl := newTestTool(t, "", ff, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "Reference     : "+ref) || !strings.Contains(got, "Dubbed dir    : "+cz) {
		t.Errorf("unexpected mixed header: %s", got)
	}
	if !strings.Contains(got, "pair: Show.S01E01.mkv <- Show.1x01.CZ.mkv") {
		t.Errorf("missing mixed pair: %s", got)
	}
	if !strings.Contains(got, "Aligned: 1, skipped: 0, failed: 0.") {
		t.Errorf("unexpected summary: %s", got)
	}
	if _, err := os.Stat(ref + ".old"); err != nil {
		t.Errorf("reference backup missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cz, "Show.1x01.CZ.mkv") + ".old"); err != nil {
		t.Errorf("dub backup missing: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(en, "Show.S01E02.mkv")); err != nil || len(data) != 1000 {
		t.Errorf("the other reference must stay untouched: %v", err)
	}
}

func TestMixedDirAndFile(t *testing.T) {
	_, en, cz, ff := batchFixture(t)
	dub := filepath.Join(cz, "Show.1x01.CZ.mkv")
	cfg := &Config{RefDir: en, Dub: dub, Mixed: true}
	cfg.Interval, cfg.MatchDist, cfg.Bitrate = 0.2, 45, "192k"
	cfg.LangRef, cfg.LangDub = "eng", "ces"
	out := &bytes.Buffer{}
	tl := newTestTool(t, "", ff, cfg, out)
	defer tl.guard.Cleanup()
	if err := tl.runBatch(); err != nil {
		t.Fatalf("runBatch: %v (out: %s)", err, out.String())
	}
	got := out.String()
	if !strings.Contains(got, "Reference dir : "+en) || !strings.Contains(got, "Dubbed        : "+dub) {
		t.Errorf("unexpected mixed header: %s", got)
	}
	if !strings.Contains(got, "pair: Show.S01E01.mkv <- Show.1x01.CZ.mkv") {
		t.Errorf("missing mixed pair: %s", got)
	}
	if _, err := os.Stat(filepath.Join(en, "Show.S01E02.mkv") + ".old"); err == nil {
		t.Error("the non-matching reference must not be touched")
	}
}

func TestResolveMixedTargets(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "Show.S01E01.mkv")
	if err := os.WriteFile(file, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "dubs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{}
	batch, err := resolveTargets([]string{file, sub}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !batch || cfg.Ref != file || cfg.DubDir != sub || !cfg.Mixed || cfg.RefDir != "" {
		t.Errorf("file+dir resolved to %+v", cfg)
	}

	cfg = &Config{}
	batch, err = resolveTargets([]string{dir, file}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !batch || cfg.RefDir != dir || cfg.Dub != file || !cfg.Mixed || cfg.Ref != "" {
		t.Errorf("dir+file resolved to %+v", cfg)
	}

	cfg = &Config{DubSuffix: ".cz"}
	if _, err := resolveTargets([]string{file, sub}, cfg); err == nil {
		t.Error("-dub-suffix must be rejected in mixed mode")
	}
}
