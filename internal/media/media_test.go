package media

import (
	"reflect"
	"testing"

	"github.com/hranicka/media-tool/internal/probe"
)

func TestExpandWhitelist(t *testing.T) {
	got := ExpandWhitelist([]string{"CS", "en", "ger"})
	want := []string{"cs", "ces", "cze", "cz", "en", "eng", "ger"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLangKept(t *testing.T) {
	wl := ExpandWhitelist([]string{"cs", "en"})

	kept := []string{"", "und", "unknown", "cs", "CES", "cze", "cz", "en", "ENG", "eng"}
	for _, tag := range kept {
		if !LangKept(tag, wl) {
			t.Errorf("tag %q should be kept", tag)
		}
	}
	dropped := []string{"ger", "rus", "jpn", "english"}
	for _, tag := range dropped {
		if LangKept(tag, wl) {
			t.Errorf("tag %q should be dropped", tag)
		}
	}
}

func TestCommentary(t *testing.T) {
	yes := []string{"Director's Cut", "Commentary", "Audio comment track", "directors commentary"}
	for _, title := range yes {
		if !Commentary(title) {
			t.Errorf("title %q should be commentary", title)
		}
	}
	no := []string{"", "Original", "DTS-HD MA 5.1", "Behind the scenes"}
	for _, title := range no {
		if Commentary(title) {
			t.Errorf("title %q should not be commentary", title)
		}
	}
}

func TestIsCoverArt(t *testing.T) {
	cases := []struct {
		codec     string
		attached  int
		wantCover bool
	}{
		{"mjpeg", 0, true},
		{"png", 0, true},
		{"h264", 1, true},
		{"h264", 0, false},
		{"hevc", 0, false},
	}
	for _, c := range cases {
		s := probe.Stream{CodecName: c.codec, Disposition: probe.Disposition{AttachedPic: c.attached}}
		if got := IsCoverArt(s); got != c.wantCover {
			t.Errorf("IsCoverArt(%s, attached=%d) = %v, want %v", c.codec, c.attached, got, c.wantCover)
		}
	}
}
