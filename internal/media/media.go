// Package media holds the stream model shared by the subcommands:
// language whitelist handling, commentary detection and cover-art rules.
package media

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hranicka/mediatool/internal/probe"
)

// Languages without a real tag are always kept by the whitelist.
var untagged = map[string]bool{"": true, "und": true, "unknown": true}

var commentaryRe = regexp.MustCompile(`(?i)comment|director`)

// langNames maps full English language names to their ISO 639-2 code.
// Some muxers tag tracks with the word ("English", "Czech") rather than
// the code ("eng", "cze"); normalizing lets the code-based whitelist match
// both spellings.
var langNames = map[string]string{
	"arabic": "ara", "bulgarian": "bul", "chinese": "chi", "croatian": "hrv",
	"czech": "cze", "danish": "dan", "dutch": "dut", "english": "eng",
	"finnish": "fin", "french": "fre", "german": "ger", "greek": "gre",
	"hebrew": "heb", "hindi": "hin", "hungarian": "hun", "indonesian": "ind",
	"italian": "ita", "japanese": "jpn", "korean": "kor", "norwegian": "nor",
	"polish": "pol", "portuguese": "por", "romanian": "rum", "russian": "rus",
	"serbian": "srp", "slovak": "slo", "spanish": "spa", "swedish": "swe",
	"thai": "tha", "turkish": "tur", "ukrainian": "ukr", "vietnamese": "vie",
}

// normalizeLang lowercases a tag and resolves a full language name to its
// ISO 639-2 code; codes and unknown tags pass through lowercased.
func normalizeLang(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	if code, ok := langNames[t]; ok {
		return code
	}
	return t
}

// ExpandWhitelist expands the requested languages to their alias spellings
// (cs -> cs/ces/cze/cz, en -> en/eng; other entries pass through normalized).
// Full names ("czech", "english") are accepted like their codes.
func ExpandWhitelist(langs []string) []string {
	var out []string
	for _, l := range langs {
		switch normalizeLang(l) {
		case "cs", "ces", "cze", "cz":
			out = append(out, "cs", "ces", "cze", "cz")
		case "en", "eng":
			out = append(out, "en", "eng")
		default:
			out = append(out, normalizeLang(l))
		}
	}
	return out
}

// LangKept reports whether a stream's language tag passes the whitelist
// (case-insensitive on both sides, full names resolved to codes;
// missing/und/unknown tags always count as kept).
func LangKept(tag string, whitelist []string) bool {
	tag = normalizeLang(tag)
	if untagged[tag] {
		return true
	}
	for _, w := range whitelist {
		if tag == normalizeLang(w) {
			return true
		}
	}
	return false
}

// Commentary reports whether a track title marks it as a commentary track.
func Commentary(title string) bool {
	return commentaryRe.MatchString(title)
}

// IsCoverArt reports whether a video stream is cover art (mjpeg/png codec
// or the attached_pic disposition) that must never act as real video.
func IsCoverArt(s probe.Stream) bool {
	switch s.CodecName {
	case "mjpeg", "png":
		return true
	}
	return s.Disposition.AttachedPic == 1
}

// LangDisplay renders a language tag for human-readable plan lines
// (untagged tracks show as "none", bash parity).
func LangDisplay(tag string) string {
	if tag == "" {
		return "none"
	}
	return tag
}

// HumanBytes formats a byte count in IEC units (1024 base), mirroring
// numfmt --to=iec ("98K", "1.5G").
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	s := fmt.Sprintf("%.1f", float64(n)/float64(div))
	return strings.TrimSuffix(s, ".0") + string("KMGTPE"[exp])
}
