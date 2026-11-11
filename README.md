# MediaTool 2.0

Single-binary media library toolkit for MKV/MP4 compaction and management on
Linux. One `mediatool` binary serves NVIDIA (NVENC), Intel (QSV/VAAPI) and
AMD (VAAPI via Mesa) machines alike — the encoder is probed at runtime, not
chosen at build time. 2.0 replaces the retired per-tool generation
(hevcconverter/ac3converter/cleaner/dupfinder) and the interim bash suite
(hevc-convert.sh, hevc-tablet.sh and friends).

## Install

Grab a binary from [GitHub Releases](../../releases) (linux/amd64, static,
no runtime dependencies beyond ffmpeg/ffprobe), or build from source:

```shell
make build      # dist/mediatool
```

## Commands

| Command | Purpose |
|---|---|
| `convert` | re-encode video to HEVC/AV1 via presets (movie, tv, tablet, camera, lowq, chromecast) |
| `clean` | remove audio/subtitle tracks of non-whitelisted languages (default: cs + en) |
| `ac3` | append AC3 640k compatibility tracks for DTS/TrueHD/FLAC audio |
| `dup` | report duplicate audio tracks (read-only) |
| `enhance` | AI-upscale with Real-ESRGAN and re-encode to HEVC 1080p |
| `version` | print the version |

Run `mediatool <command> -h` for the full flag list of each command.

### convert

```
mediatool convert [-p movie|tv|tablet|camera|lowq|chromecast]
                  [-c hevc|av1] [-e auto|nvenc|qsv|vaapi|sw] [-q cq] [-r height]
                  [-a copy|aac[:bitrate[:channels]]|ac3[:bitrate]] [-tonemap]
                  [-vaapi_device dev] [-i list] [-f] [-n] <dir|file>
```

Presets bundle the common workflows; every preset value is overridable by
an explicitly passed flag (`-p tablet -r 1080p -q 28 -a copy`):

| preset | quality | scale | HDR | audio |
|---|---|---|---|---|
| `movie` (default) | CQ 22 quality target | – | kept | copy |
| `tv` | CQ 26 | 1080p | → SDR | AC3 compat appended |
| `tablet` | CQ 33 | 720p | → SDR | AAC 192k stereo |
| `camera` | CQ 28, capped at ~65 % of the source bitrate | – | – | AAC 128k |
| `lowq` | CQ 31, capped at ~50 % | – | – | copy |
| `chromecast` | CQ 22, 15M cap, level 4.1, written as a new `*_CC4_FIXED.mkv` (originals untouched) | – | – | copy |

Lower CQ means higher quality (and bigger files). `movie` defaults to
CQ 22 — a quality-leaning target that still compacts well; override with
`-q` (`-q 18` for action, grainy film or near-transparent 1080p/4K HDR,
`-q 26` for smaller files). `movie` keeps the source resolution and HDR.
NVENC encodes use spatial + temporal AQ, 32-frame lookahead and fullres
multipass to spend bits better on motion and grain.

- `-c av1` targets AV1 instead of HEVC (hardware `av1_nvenc`/`av1_qsv`/
  `av1_vaapi` where available, `libsvtav1` software fallback); HEVC stays
  the default output. The chromecast preset is HEVC-only (the target device
  decodes HEVC). GPUs without AV1 hardware encode (e.g. the Intel N100)
  fall back to slow software encoding.
- Sources: H.264 and VC-1 (`vc1`, including `wmv3`) convert without
  flags; anything else needs `-f`.
- **Tonemap** (`-tonemap`, on by default in tv/tablet): HDR10/HLG sources
  re-map to SDR bt709 (bt2390) — on NVENC via the GPU-resident CUDA chain
  (`tonemap_cuda`), otherwise in software through libplacebo when the
  ffmpeg build has it; with neither available, HDR files are skipped
  untouched. Without tonemapping, HDR10/HLG metadata is preserved (p010
  for 10-bit); Dolby Vision sources are skipped (`-f` forces, losing the
  DV layer); HDR10+ dynamic metadata is dropped (static HDR10 stays).
- **Audio target** (`-a`): `copy` keeps all audio as-is; `aac[:bitrate[:channels]]`
  transcodes non-AAC tracks (existing AAC tracks are copied; `-a aac:192k:2`
  = 192 kbps stereo); `ac3[:bitrate]` appends AC3 compatibility tracks by
  the `ac3` command's rules, in the same encode pass.
- **Track cleanup** (`-l cs,en`): audio and subtitle tracks outside the
  language whitelist are dropped during the encode, by the `clean`
  command's rules (whitelist expansion, untagged and reserved `qaa`–`qtz`
  tracks always stay, sole tracks are never removed). The plan lines list
  every removal; a stream-count gate verifies the output. E.g. the classic
  one-shot library sweep: `mediatool convert -q 22 -l cs,en <dir>`.
- **Deinterlace is automatic**: sources flagged interlaced (field order
  tt/tb/bt/bb) are yadif-deinterlaced before scaling, on every backend.
- `-r 1080p` downscales only (never upscales), aspect ratio preserved.
- Container selection: MKV sources stay MKV. Other sources mux to MP4
  (with `+faststart`) only when they carry video+audio alone; anything with
  subtitles, teletext/data or attachments goes to MKV so nothing is lost.
  The output extension follows the container (`part1.ts` → `part1.mkv`,
  `cam.ts` → `cam.mp4`), the original is kept as `<source>.old`, and the
  rename is shown as an `output:` line. The walk picks up `.mkv/.mp4` plus
  the camera formats `.mts/.m2ts/.ts/.mpg/.mpeg/.avi/.mov/.wmv`; MPEG-TS/AVI
  inputs get `+genpts`. The converted file keeps the source's mtime.
- Files with several real video streams (3D MVC) are skipped; cover-art
  streams (mjpeg/png/attached_pic) are dropped from the encode.
- Retry ladder: hardware decode with subtitles → without → software decode
  (tonemapped HDR files use the CUDA chain first, then libplacebo).
- Outputs are adopted only when smaller and at least 99 % of the source
  duration (and, with appended AC3 tracks, only when every track survived);
  otherwise the original stays untouched.

### clean

```
mediatool clean [-l cs,en] [-e] [-old] [-i list] [-n] <dir|file>
```

Removes audio and subtitle tracks whose language tag is outside the
whitelist (`cs,en` expand to cs/ces/cze/cz and en/eng; full names like
`English`/`Czech` match too). Missing/und/unknown tags and the reserved
local-use codes `qaa`–`qtz` (broadcast "original audio") always count as
kept. A stream is kept when it is the only track of its type unless `-e`
(flagged with a notice). Cover-art video streams are always removed. Pure
`-c copy` remux — never re-encodes. Verified by duration, size and
stream-count gates. Only `.mkv`/`.mp4` are processed; broadcast dumps
(`.ts/.mts/.m2ts/.mpg/.mpeg`) are skipped — clean would drop their
teletext/data streams — use `convert` for those. Note: MP4 files usually
carry no language tags, so cleaning them is mostly a no-op except
cover-art removal.

With `-old` the command switches to a deletion mode: it removes the
`.old` backup files of previous runs instead of cleaning tracks (the
target may be a directory or a single `.old` file; `-l`/`-e` do not
apply). Honors the ignore rules and `-n`; the summary reports the freed
space.

### ac3

```
mediatool ac3 [-b 448000] [-l lang] [-i list] [-n] <dir|file>
```

For audio in DTS/TrueHD/FLAC without an AC3 track of the same language,
appends an AC3 640k track (video and everything else copied). An existing
AC3 track counts as "already converted" for its language unless it is a
commentary track, below `-b` (bit/s, so the default 448000 = 448 kbps), or
has an unknown bitrate with fewer than 6 channels. E-AC3 is never converted
(both playback targets decode it).

### dup

```
mediatool dup [-i list] <dir>
```

Read-only report of audio tracks sharing codec, bitrate, channel count and
language tag within one file. Useful before `clean`.

### dedup

```
mediatool dedup [-interval 2] [-min-match 0.8] [-max-dist 0.15] [-tolerance 5] [-i list] <dir>
```

Read-only report of media files that carry the **same content** across the
library — different encodes, sizes, extensions or names of one movie or
episode, `.old` backups excluded. Instead of hashing files, it samples a
frame every `-interval` seconds (downscaled grayscale), perceptually hashes
each one (median-threshold, robust against encoder noise, brightness
shifts and resolution changes) and matches the sequences with a windowed
walk, so shifted starts, cut scenes and framerate variants are tolerated.

Only files whose durations are within `-tolerance` percent (10 s floor)
are compared, so most of the library never pays for frame extraction; a
pair matches when at least `-min-match` of the frames match reliably
(hamming distance ≤ `-max-dist` of the hash bits). Shifted starts are
followed within the walk window (±100 s at the default interval). Files
without a container duration pair with everything (the run notes them).
Output groups the
matches, marks the suggested keeper (highest resolution, then the larger
file) and totals the reclaimable space — deletion stays manual. Groups
whose members' frames are highly self-similar (synthetic patterns, long
static scenes) carry a caution note: matches may be spurious there.

Frame extraction decodes the whole file, so expect the hashing phase to
run a while for large libraries; everything else is cheap.

### align

```
mediatool align [-interval 0.2] [-match-dist 45] [-bitrate 192k]
                [-lang-ref eng] [-lang-dub ces] [-n] <reference> <dubbed> [output]
```

Appends a **dubbed audio track to a reference video** by scene matching
(the `REF+dub.mkv` output carries the reference video, its own audio and
subtitles plus the resampled dub as a second AAC track). Frames sampled
from both files are perceptually hashed; each dubbed frame matches the
best reference frame within a windowed neighborhood (±10 s at the default
interval, so shifted starts must stay inside that bound), a quadratic fit
with outlier rejection turns the matches into a smooth monotonic timing
map (tolerating different framerates and cut scenes), and the dub audio
is resampled onto that map before muxing. The run refuses implausible
maps (span outside 80–125% of the dub's duration — the signature of
self-similar content where matches map noise) and warns when frames are
highly self-similar. `-n` stops after the
timing map and prints it — check the offsets before committing to the
encode. Port of the align_audio.py workflow.

### enhance

```
mediatool enhance [-q 22] [-engine path] [-vaapi_device dev] [-i list] [-n] <dir>
```

Extracts frames with hardware decoding, upscales them 2× with the
Real-ESRGAN anime model (ncnn Vulkan) and encodes to HEVC 1080p
(`scale=-2:1080`, aspect-preserving), written as MKV for both MKV and MP4
inputs. Output: `<stem>_enhanced_1080p_hevc.mkv` next to the source. The
engine binary is downloaded on first use to `~/.local/share/mediatool/`
(override with `-engine` or `REALESRGAN_BIN`). Frame directories are
cleaned on exit and on interrupt.

## Ignore files

A file is skipped when its path contains **any** entry from any of these
(union; one plain substring per line, matched literally against the full
path — directory and file names both count; `#` comments allowed):

| File | Read by |
|---|---|
| `.mediatool-ignore` in the target dir | all commands |
| `.mediatool-convert-ignore` / `.mediatool-clean-ignore` / `.mediatool-ac3-ignore` / `.mediatool-enhance-ignore` | their command — per-command ignore lists, e.g. "not to clean" vs "not to re-encode" |
| `.hevcconverter-ignore` / `.cleaner-ignore` / `.ac3converter-ignore` / `.enhance-ignore` | deprecated pre-2.0 per-command names, still honored with a rename warning |
| `-i "a,b"` CLI list | all commands |

Files are looked up in the target directory **and every ancestor up to the
filesystem root** (like a flat gitignore hierarchy; for a single-file target
the search starts at the file's directory), so an ignore file in `/foo` or
`/foo/bar` also governs `mediatool clean /foo/bar/baz`.

## Conventions

- **`.old` backups**: in-place replacements never clobber an existing
  `<file>.old` — convert skips such files up front; clean and ac3 refuse
  the swap and keep the original when one exists. Pass `-no-backup` to
  convert/clean/ac3 to replace files in place without creating a backup
  (the original is removed; `mediatool clean -old` removes the backups
  a previous run left behind).
- **Safety gates**: outputs are adopted only after duration, size and
  stream-count checks.
- **Dry run**: every modifying command accepts `-n` and prints its plan.

## Output & colors

When stdout and stderr are both terminals, the output renders for humans:
a settings header, a line per processed file with a status symbol (green ✓
processed, red ✗ failed, yellow ⚠ notices), `[i/n]` position markers, a
per-file progress bar driven by ffmpeg's own `time=`/`speed=` stats
(convert, clean, ac3, enhance), failure lines with the captured ffmpeg
error tail, and a closing summary with size aggregates, the failure list
and elapsed time. The enhance engine download shows a byte progress bar.

By default only files that are (or would be) processed get a permanent
line; skipped/untouched files collapse into a single dim "walking" line
that rewrites in place (`– [i/n] path — reason`) so you can see the run
moving through the library without a scroll of gray skips. `-v` shows
every skip/OK line permanently and swaps the progress bar for ffmpeg's
raw stats passthrough.

Piped or redirected output stays plain — line-for-line identical to the
earlier releases — so scripts and greps keep working. Color control
follows the usual conventions: `NO_COLOR` disables colors, `CLICOLOR_FORCE=1`
forces them, `TERM=dumb` disables them.

## Requirements

- ffmpeg / ffprobe (`FFMPEG_BIN`/`FFPROBE_BIN` env overrides, else
  Jellyfin's build, common locations, PATH)
- hardware encoders: NVIDIA driver for nvenc; the `render` group for
  `/dev/dri/renderD128` on Intel/AMD (VAAPI); Intel mfx runtime for QSV.
  A missing backend is probed away — everything falls back to libx265.
- jq is not needed — 2.0 parses ffprobe JSON natively

## Development

```shell
make install   # staticcheck
make test      # vet + staticcheck + race tests
make lint      # gofmt check
make build     # dist/mediatool
```

CI runs the same checks on every push/PR; tagged releases (`v*`) build the
linux/amd64 binaries via GoReleaser and publish them to GitHub Releases.
