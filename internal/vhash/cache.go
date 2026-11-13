package vhash

import (
	"bufio"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	cacheMagic   = "MTALGN1\n"
	cacheVersion = 1 // bump when FrameHash/Extract changes its signature
	maxHeaderLen = 64 << 10
)

// cacheHeader describes one cached signature list. The source file's size
// and mtime invalidate the entry when the media changes; Interval and
// Version invalidate it when the sampling or hash algorithm changes.
type cacheHeader struct {
	Version   int     `json:"version"`
	Interval  float64 `json:"interval"`
	Path      string  `json:"path"`
	Size      int64   `json:"size"`
	ModTimeNS int64   `json:"mtime_ns"`
	Frames    int     `json:"frames"`
	HashLen   int     `json:"hash_len"`
}

// Cache stores frame signatures on disk so a dry run's expensive full
// decodes can be reused by a later real run. A zero Cache (no Dir) is
// disabled and every operation is a no-op.
//
// Files are named <sha256(abs path)>.cache and hold a small JSON header
// plus the raw 1024-bit hashes; no frames are written.
type Cache struct {
	Dir string
}

// DefaultCacheDir returns $MEDIATOOL_CACHE_DIR, else the XDG cache dir
// under mediatool/align, else a temp-dir fallback.
func DefaultCacheDir() string {
	if dir := os.Getenv("MEDIATOOL_CACHE_DIR"); dir != "" {
		return dir
	}
	if dir, err := os.UserCacheDir(); err == nil && dir != "" {
		return filepath.Join(dir, "mediatool", "align")
	}
	return filepath.Join(os.TempDir(), "mediatool-align-cache")
}

// Enabled reports whether the cache has a directory.
func (c Cache) Enabled() bool { return c.Dir != "" }

// pathFor returns the cache file of a media path.
func (c Cache) pathFor(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	sum := sha256.Sum256([]byte(abs))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])+".cache")
}

// Load returns the cached signatures of path when the entry matches the
// file's size, mtime and interval and passes the integrity checks.
func (c Cache) Load(path string, interval float64) ([][HashLen]byte, bool) {
	if !c.Enabled() {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, false
	}
	f, err := os.Open(c.pathFor(path))
	if err != nil {
		return nil, false
	}
	defer f.Close()

	var magic [len(cacheMagic)]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != cacheMagic {
		return nil, false
	}
	var lenBuf [4]byte
	if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
		return nil, false
	}
	headerLen := binary.BigEndian.Uint32(lenBuf[:])
	if headerLen == 0 || headerLen > maxHeaderLen {
		return nil, false
	}
	headerBytes := make([]byte, headerLen)
	if _, err := io.ReadFull(f, headerBytes); err != nil {
		return nil, false
	}
	var hdr cacheHeader
	if err := json.Unmarshal(headerBytes, &hdr); err != nil {
		return nil, false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if hdr.Version != cacheVersion || hdr.Interval != interval ||
		hdr.HashLen != HashLen ||
		filepath.Clean(hdr.Path) != filepath.Clean(abs) ||
		hdr.Size != info.Size() || hdr.ModTimeNS != info.ModTime().UnixNano() ||
		hdr.Frames < 0 || hdr.Frames > 10_000_000 {
		return nil, false
	}
	expected := int64(len(cacheMagic)+4+len(headerBytes)) + int64(hdr.Frames)*HashLen
	if st, err := f.Stat(); err != nil || st.Size() != expected {
		return nil, false
	}
	hashes := make([][HashLen]byte, hdr.Frames)
	for i := range hashes {
		if _, err := io.ReadFull(f, hashes[i][:]); err != nil {
			return nil, false
		}
	}
	return hashes, true
}

// Store writes the signatures atomically (temp file + rename), so a
// concurrent reader sees either the old complete entry or the new one.
func (c Cache) Store(path string, interval float64, hashes [][HashLen]byte) error {
	if !c.Enabled() {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	hdr := cacheHeader{
		Version:   cacheVersion,
		Interval:  interval,
		Path:      abs,
		Size:      info.Size(),
		ModTimeNS: info.ModTime().UnixNano(),
		Frames:    len(hashes),
		HashLen:   HashLen,
	}
	headerBytes, err := json.Marshal(hdr)
	if err != nil {
		return err
	}
	if len(headerBytes) > maxHeaderLen {
		return fmt.Errorf("cache header too large (%d bytes)", len(headerBytes))
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	target := c.pathFor(path)
	tmp, err := os.CreateTemp(c.Dir, filepath.Base(target)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename

	bw := bufio.NewWriterSize(tmp, 256<<10)
	if _, err := bw.WriteString(cacheMagic); err != nil {
		tmp.Close()
		return err
	}
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(headerBytes)))
	if _, err := bw.Write(lenBuf[:]); err != nil {
		tmp.Close()
		return err
	}
	if _, err := bw.Write(headerBytes); err != nil {
		tmp.Close()
		return err
	}
	for i := range hashes {
		if _, err := bw.Write(hashes[i][:]); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}

// Remove deletes the cached entry for path (also after the media file was
// replaced, when its size/mtime no longer match).
func (c Cache) Remove(path string) error {
	if !c.Enabled() {
		return nil
	}
	err := os.Remove(c.pathFor(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// cacheFile reports whether name is one of our cache files (including
// temp files left by an interrupted atomic write).
func cacheFile(name string) bool {
	return strings.HasSuffix(name, ".cache") || strings.Contains(name, ".cache.tmp")
}

// Stats reports the number and total size of the cache files.
func (c Cache) Stats() (files int, bytes int64, err error) {
	if !c.Enabled() {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() || !cacheFile(e.Name()) {
			continue
		}
		info, statErr := e.Info()
		if statErr != nil {
			continue
		}
		files++
		bytes += info.Size()
	}
	return files, bytes, nil
}

// Clear removes every cache file and left-over temp file. Other files in
// the directory are left alone, so -cache-dir can point at a shared folder.
func (c Cache) Clear() (files int, bytes int64, err error) {
	if !c.Enabled() {
		return 0, 0, nil
	}
	entries, err := os.ReadDir(c.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() || !cacheFile(e.Name()) {
			continue
		}
		info, statErr := e.Info()
		if statErr == nil {
			bytes += info.Size()
		}
		if err := os.Remove(filepath.Join(c.Dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return files, bytes, err
		}
		files++
	}
	os.Remove(c.Dir) // only succeeds when the directory is empty
	return files, bytes, nil
}
