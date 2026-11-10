package dup

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hranicka/mediatool/internal/ignore"
	"github.com/hranicka/mediatool/internal/run"
)

type fakeFF struct {
	probeFor func(src string) (string, bool)
}

func (f *fakeFF) Output(name string, args ...string) ([]byte, error) {
	if name == "ffprobe" && f.probeFor != nil {
		if json, ok := f.probeFor(args[len(args)-1]); ok {
			return []byte(json), nil
		}
	}
	return []byte(`{"streams":[],"format":{}}`), nil
}

func (f *fakeFF) Stream(name string, args ...string) ([]byte, error) {
	return nil, nil
}

func audioJSON(codec, br string, ch int, lang string) string {
	return `{"index":0,"codec_name":"` + codec + `","codec_type":"audio","bit_rate":"` + br + `","channels":` + strconv.Itoa(ch) + `,"tags":{"language":"` + lang + `"}}`
}

func dupJSON() string {
	return `{"streams":[` +
		`{"index":0,"codec_name":"h264","codec_type":"video"},` +
		audioJSON("aac", "192000", 2, "eng") + `,` +
		audioJSON("aac", "192000", 2, "eng") + `,` +
		audioJSON("aac", "192000", 2, "ger") +
		`],"format":{"duration":"600.0","size":"1000"}}`
}

func cleanJSON() string {
	return `{"streams":[` +
		`{"index":0,"codec_name":"h264","codec_type":"video"},` +
		audioJSON("aac", "192000", 2, "eng") +
		`],"format":{"duration":"600.0","size":"1000"}}`
}

func TestDupReport(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "dup.mkv")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: func(string) (string, bool) { return dupJSON(), true }}
	out := &bytes.Buffer{}
	tl := &tool{
		r: ff, ffprobeBin: "ffprobe",
		cfg:     &Config{Dir: dir},
		ignores: ignore.Load(dir, "dup", nil),
		out:     out,
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if !strings.Contains(out.String(), "Duplicate audio tracks in "+src+": #0 #1 (codec/bitrate/channels/language: aac|192000|2|eng)") {
		t.Errorf("dup report missing: %s", out.String())
	}
	if !strings.Contains(out.String(), "Files with duplicated audio tracks: 1") {
		t.Errorf("summary missing: %s", out.String())
	}
}

func TestDupNoReport(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "clean.mkv")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	ff := &fakeFF{probeFor: func(string) (string, bool) { return cleanJSON(), true }}
	out := &bytes.Buffer{}
	tl := &tool{
		r: ff, ffprobeBin: "ffprobe",
		cfg:     &Config{Dir: dir},
		ignores: ignore.Load(dir, "dup", nil),
		out:     out,
	}
	if err := tl.runAll(); err != nil {
		t.Fatalf("runAll: %v", err)
	}

	if strings.Contains(out.String(), "Duplicate audio tracks") {
		t.Errorf("no dups expected: %s", out.String())
	}
	if !strings.Contains(out.String(), "Files with duplicated audio tracks: 0") {
		t.Errorf("summary missing: %s", out.String())
	}
}

var _ run.Runner = &fakeFF{}
