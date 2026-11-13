package vhash

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testHashes(n int) [][HashLen]byte {
	out := make([][HashLen]byte, n)
	for i := range out {
		out[i][0] = byte(i)
		out[i][HashLen-1] = byte(n - i)
	}
	return out
}

func equalHashes(a, b [][HashLen]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: filepath.Join(dir, "cache")}
	hashes := testHashes(25)
	if err := c.Store(media, 0.2, hashes); err != nil {
		t.Fatalf("Store: %v", err)
	}
	got, ok := c.Load(media, 0.2)
	if !ok {
		t.Fatal("expected a cache hit")
	}
	if !equalHashes(got, hashes) {
		t.Error("cached hashes differ")
	}
	files, size, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if files != 1 || size == 0 {
		t.Errorf("Stats = %d files, %d bytes; want 1 file, >0 bytes", files, size)
	}
}

func TestCacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: filepath.Join(dir, "cache")}
	hashes := testHashes(10)
	if err := c.Store(media, 0.2, hashes); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(media, 0.5); ok {
		t.Error("a different interval must miss")
	}

	// size change
	if err := os.WriteFile(media, []byte("media-changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(media, 0.2); ok {
		t.Error("a changed size must miss")
	}

	// mtime change on the same size
	if err := os.WriteFile(media, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Store(media, 0.2, hashes); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(media, future, future); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(media, 0.2); ok {
		t.Error("a changed mtime must miss")
	}
}

func TestCacheCorruptEntriesMiss(t *testing.T) {
	dir := t.TempDir()
	media := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(media, []byte("media"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Cache{Dir: filepath.Join(dir, "cache")}
	if err := c.Store(media, 0.2, testHashes(10)); err != nil {
		t.Fatal(err)
	}
	cachePath := c.pathFor(media, 0.2)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, data[:len(data)-4], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(media, 0.2); ok {
		t.Error("a truncated entry must miss")
	}
	if err := os.WriteFile(cachePath, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(media, 0.2); ok {
		t.Error("a garbage entry must miss")
	}
}

func TestCacheRemoveAndClear(t *testing.T) {
	dir := t.TempDir()
	c := Cache{Dir: filepath.Join(dir, "cache")}
	mediaA := filepath.Join(dir, "a.mkv")
	mediaB := filepath.Join(dir, "b.mkv")
	for _, p := range []string{mediaA, mediaB} {
		if err := os.WriteFile(p, []byte("media"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := c.Store(p, 0.2, testHashes(5)); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Remove(mediaA, 0.2); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Load(mediaA, 0.2); ok {
		t.Error("removed entry must miss")
	}
	if _, ok := c.Load(mediaB, 0.2); !ok {
		t.Error("the other entry must stay")
	}

	// an interrupted atomic write leaves a temp file behind; cleanup must
	// remove it too
	tmp := filepath.Join(c.Dir, "deadbeef.cache.tmp123")
	if err := os.WriteFile(tmp, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(c.Dir, "keep-me.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	files, size, err := c.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if files != 2 || size == 0 {
		t.Errorf("Stats = %d files, %d bytes; want 2 files (entry + temp)", files, size)
	}
	removed, freed, err := c.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if removed != 2 || freed == 0 {
		t.Errorf("Clear = %d files, %d bytes; want 2 files", removed, freed)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Error("Clear must leave unrelated files alone")
	}
	if files, _, _ := c.Stats(); files != 0 {
		t.Errorf("cache must be empty, got %d files", files)
	}
}

func TestDefaultCacheDirEnv(t *testing.T) {
	t.Setenv("MEDIATOOL_CACHE_DIR", "/custom/cache")
	if got := DefaultCacheDir(); got != "/custom/cache" {
		t.Errorf("DefaultCacheDir = %q, want /custom/cache", got)
	}
}
