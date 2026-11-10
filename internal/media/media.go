// Package media holds the stream model shared by the subcommands:
// language whitelist handling, commentary detection and cover-art rules.
package media

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hranicka/media-tool/internal/probe"
)

// Languages without a real tag are always kept by the whitelist.
var untagged = map[string]bool{"": true, "und": true, "unknown": true}

var commentaryRe = regexp.MustCompile(`(?i)comment|director`)

// ExpandWhitelist expands the requested languages to their alias spellings
// (cs -> cs/ces/cze/cz, en -> en/eng; other entries pass through lowercased).
func ExpandWhitelist(langs []string) []string {
	var out []string
	for _, l := range langs {
		switch strings.ToLower(l) {
		case "cs":
			out = append(out, "cs", "ces", "cze", "cz")
		case "en":
			out = append(out, "en", "eng")
		default:
			out = append(out, strings.ToLower(l))
		}
	}
	return out
}

// LangKept reports whether a stream's language tag passes the whitelist
// (case-insensitive on both sides; missing/und/unknown tags always count
// as kept).
func LangKept(tag string, whitelist []string) bool {
	tag = strings.ToLower(tag)
	if untagged[tag] {
		return true
	}
	for _, w := range whitelist {
		if tag == strings.ToLower(w) {
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
