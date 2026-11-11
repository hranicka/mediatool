// Presets bundle the flag defaults of the common conversion workflows, so
// a single -p value covers the whole job (library compaction, living-room
// copies, tablet files, camera dumps). Every preset value is overridable
// by an explicitly passed flag. They replace the pre-2.x -p quality modes:
// lowq and chromecast survive as presets, movie is the default.
package convert

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/hranicka/mediatool/internal/encoders"
)

// Preset holds the default conversion plan of one named workflow.
type Preset struct {
	Mode        encoders.Mode
	CQ          int    // 0 = the mode's default quality value
	ScaleHeight int    // 0 = never scale
	Tonemap     bool   // HDR sources re-map to SDR bt709
	Audio       string // audio target spec (parseAudio)
}

var presets = map[string]Preset{
	// library default: keep resolution + HDR, keep all audio as-is. CQ 22
	// leans quality; override with -q (e.g. -q 18 higher, -q 26 smaller).
	"movie": {Mode: encoders.ModeMovie, CQ: 22},
	// living-room 1080p SDR copy with compatibility audio
	"tv": {Mode: encoders.ModeMovie, ScaleHeight: 1080, Tonemap: true, Audio: "ac3"},
	// small player files: 720p SDR, stereo AAC (heir of hevc-tablet-*.sh)
	"tablet": {Mode: encoders.ModeMovie, CQ: 33, ScaleHeight: 720, Tonemap: true, Audio: "aac:192k:2"},
	// camera dumps: bitrate-capped, deinterlaced, AAC audio
	// (heir of hevc-convert.sh + the camera mode)
	"camera":     {Mode: encoders.ModeCamera, Audio: "aac:128k"},
	"lowq":       {Mode: encoders.ModeLowQ},
	"chromecast": {Mode: encoders.ModeChromecast},
}

// Lookup validates a -p preset name.
func Lookup(name string) (Preset, error) {
	p, ok := presets[name]
	if !ok {
		return Preset{}, fmt.Errorf("unknown preset %q (want one of: %s)", name, strings.Join(presetNames(), ", "))
	}
	return p, nil
}

// applyPreset fills the unset flag values from the preset; an explicitly
// passed flag always wins. scaleArg and audioSpec are the raw flag
// strings, filled from the preset when unset.
func applyPreset(p Preset, set map[string]bool, cfg *Config, scaleArg, audioSpec *string) {
	cfg.Mode = p.Mode
	if !set["q"] {
		cfg.CQ = p.CQ
	}
	if !set["r"] {
		*scaleArg = ""
		if p.ScaleHeight > 0 {
			*scaleArg = fmt.Sprintf("%dp", p.ScaleHeight)
		}
	}
	if !set["tonemap"] {
		cfg.Tonemap = p.Tonemap
	}
	if !set["a"] {
		*audioSpec = p.Audio
	}
}

func presetNames() []string {
	return []string{"movie", "tv", "tablet", "camera", "lowq", "chromecast"}
}

// effectiveCQ is the preset's quality value, falling back to the mode
// default when the preset does not override it.
func (p Preset) effectiveCQ() int {
	if p.CQ != 0 {
		return p.CQ
	}
	return p.Mode.DefaultCQ()
}

// presetHelp lists the preset names in canonical order for the -p flag.
func presetHelp() string {
	return strings.Join(presetNames(), ", ")
}

// qualityHelp lists each preset's effective default CQ in the same order
// as presetHelp, so the -p and -q flags stay aligned. Both are derived
// from the presets table so the help can never drift from the defaults.
func qualityHelp() string {
	names := presetNames()
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s %d", name, presets[name].effectiveCQ()))
	}
	return strings.Join(parts, ", ")
}

// audioTarget describes what happens to a converted file's audio streams.
type audioTarget struct {
	kind     string // "copy", "aac" or "ac3"
	bitrate  string // ffmpeg-style "192k" (aac/ac3 only)
	channels int    // stereo downmix channel count, 0 = keep layout (aac only)
}

var bitrateRe = regexp.MustCompile(`^(\d+)([kKmM]?)$`)

// parseAudio parses the -a audio target spec:
// copy | aac[:bitrate[:channels]] | ac3[:bitrate]. An empty spec (a preset
// without an audio entry) means copy. A bare numeric bitrate means kbps
// (192 -> 192k).
func parseAudio(spec string) (audioTarget, error) {
	if spec == "" {
		return audioTarget{kind: "copy"}, nil
	}
	parts := strings.Split(spec, ":")
	switch parts[0] {
	case "copy":
		if len(parts) != 1 {
			return audioTarget{}, fmt.Errorf("audio target copy takes no parameters")
		}
		return audioTarget{kind: "copy"}, nil
	case "aac":
		t := audioTarget{kind: "aac", bitrate: "192k"}
		if len(parts) > 1 && parts[1] != "" {
			br, err := parseBitrate(parts[1])
			if err != nil {
				return audioTarget{}, fmt.Errorf("audio target %q: %v", spec, err)
			}
			t.bitrate = br
		}
		if len(parts) > 2 && parts[2] != "" {
			ch, err := strconv.Atoi(parts[2])
			if err != nil || ch <= 0 {
				return audioTarget{}, fmt.Errorf("audio target %q: invalid channel count %q", spec, parts[2])
			}
			t.channels = ch
		}
		if len(parts) > 3 {
			return audioTarget{}, fmt.Errorf("audio target %q: too many parameters", spec)
		}
		return t, nil
	case "ac3":
		t := audioTarget{kind: "ac3", bitrate: "640k"}
		if len(parts) > 1 && parts[1] != "" {
			br, err := parseBitrate(parts[1])
			if err != nil {
				return audioTarget{}, fmt.Errorf("audio target %q: %v", spec, err)
			}
			t.bitrate = br
		}
		if len(parts) > 2 {
			return audioTarget{}, fmt.Errorf("audio target %q: too many parameters", spec)
		}
		return t, nil
	default:
		return audioTarget{}, fmt.Errorf("unknown audio target %q (want copy, aac[:bitrate[:channels]] or ac3[:bitrate])", spec)
	}
}

func parseBitrate(s string) (string, error) {
	m := bitrateRe.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf("invalid bitrate %q", s)
	}
	if m[2] == "" {
		return m[1] + "k", nil // bare digits mean kbps
	}
	return s, nil
}

// desc renders the target for the header and plan lines.
func (a audioTarget) desc() string {
	switch a.kind {
	case "copy":
		return "copy"
	case "aac":
		d := "aac " + a.bitrate
		switch a.channels {
		case 0:
		case 2:
			d += " stereo"
		default:
			d += fmt.Sprintf(" %dch", a.channels)
		}
		return d
	default:
		return "AC3 compat " + a.bitrate
	}
}
