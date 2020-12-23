# MediaTool

Application searches for MKV (Matroska) files which contain audio streams
in DTS, TrueHD or E-AC3 codecs. If such a file does not contain AC3 stream
of the same language (except commentary tracks), file is being converted
and AC3 track is appended in the file.

### Requirements

* ffmpeg
* ffprobe (installed along with ffmpeg)

### Usage

```
make
./build/converter -help
```
