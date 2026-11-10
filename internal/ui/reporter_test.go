package ui

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// plain exercises the reporter the way redirected (non-interactive) runs
// do: every rendering must stay byte-compatible with the pre-ui output.
func plain() (*Reporter, *bytes.Buffer) {
	var buf bytes.Buffer
	return New(&buf, Profile{}), &buf
}

// pretty exercises the reporter the way interactive terminal runs do.
func pretty() (*Reporter, *bytes.Buffer) {
	var buf bytes.Buffer
	return New(&buf, Profile{Interactive: true, Color: true, UTF8: true, Width: 100}), &buf
}

func TestPlainHeader(t *testing.T) {
	rep, buf := plain()
	rep.Header([]KV{
		{"FFmpeg binary", "ffmpeg"},
		{"Ignore entries", "3"},
		{"Extra", "DRY RUN"},
	})
	want := "FFmpeg binary : ffmpeg\n" +
		"Ignore entries: 3\n" +
		"Extra         : DRY RUN\n" +
		strings.Repeat("=", 42) + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPlainEvents(t *testing.T) {
	rep, buf := plain()
	rep.Skip(2, 42, "/a.mkv", "ignored")
	rep.Skip(1, 1, "/s.mkv", "cannot probe: boom")
	rep.Notice(3, 42, "/b.mkv", "Forcing re-encode of vc1 source")
	rep.Would("Would convert", 4, 42, "/c.mkv", "1.0M, CQ 26 quality target")
	rep.Would("Would convert", 5, 42, "/d.mkv", "9.5M",
		"append AC3 640k for audio #0 (dts, lang 'cs')")
	rep.Start("Processing", 6, 42, "/e.mkv", "4.2G", 600*time.Second, "plan: CQ 26 quality target")
	rep.Progress(time.Second, "1.2x") // must be a no-op when redirected
	rep.Detail("HDR10 source: tonemapping to SDR bt709 (bt2390)")
	rep.Attempt("Nvidia NVENC", "hw decode", 1, 3)
	rep.AttemptFailed("hw decode", 1, 3)
	rep.Success("Converted", "/e.mkv", 4500000000, 1900000000, true, 0)
	rep.Success("Created", "/f.mkv", 0, 0, false, 0)
	rep.Fail("failed to convert /g.mkv (left untouched)", "")
	rep.Raw(Neutral, "Testing encoder: %s...\n", "Nvidia NVENC")
	rep.Note("  note: HDR10+ dynamic metadata will be dropped")

	want := strings.Join([]string{
		"Skipping (ignored): /a.mkv",
		"Skipping (cannot probe: boom): /s.mkv",
		"Forcing re-encode of vc1 source: /b.mkv",
		"Would convert: /c.mkv (1.0M, CQ 26 quality target)",
		"Would convert: /d.mkv (9.5M)",
		"  - append AC3 640k for audio #0 (dts, lang 'cs')",
		"Processing: /e.mkv (4.2G)",
		"  plan: CQ 26 quality target",
		"  HDR10 source: tonemapping to SDR bt709 (bt2390)",
		"  encoding with Nvidia NVENC (attempt 1/3: hw decode)...",
		"  attempt 1 (hw decode) failed...",
		"Converted: /e.mkv (4.2G -> 1.8G, backup: /e.mkv.old)",
		"Created: /f.mkv (original kept)",
		"Error: failed to convert /g.mkv (left untouched)",
		"Testing encoder: Nvidia NVENC...",
		"",
	}, "\n")
	if got := buf.String(); got != want {
		t.Errorf("got:\n%q\nwant:\n%q", got, want)
	}
}

func TestPlainSummary(t *testing.T) {
	rep, buf := plain()
	rep.Summary("Done.", "Converted", "skipped", Outcome{Done: 38, Skipped: 3, Failed: 1})
	want := strings.Repeat("=", 42) + "\nDone. Converted: 38, skipped: 3, failed: 1.\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	rep, buf = plain()
	rep.Summary("Done.", "Files with duplicated audio tracks", "", Outcome{Done: 1})
	want = strings.Repeat("=", 42) + "\nDone. Files with duplicated audio tracks: 1.\n"
	if got := buf.String(); got != want {
		t.Errorf("dup summary: got %q, want %q", got, want)
	}

	rep, buf = plain()
	rep.Summary("Batch processing complete!", "Enhanced", "skipped", Outcome{Done: 2})
	want = strings.Repeat("=", 42) + "\nBatch processing complete! Enhanced: 2, skipped: 0, failed: 0.\n"
	if got := buf.String(); got != want {
		t.Errorf("enhance summary: got %q, want %q", got, want)
	}
}

func TestPlainNoteGoesToStderr(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	rep, _ := plain()
	rep.Note("  note: HDR10+ dynamic metadata will be dropped")
	os.Stderr = old
	w.Close()

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "  note: HDR10+ dynamic metadata will be dropped\n" {
		t.Errorf("stderr note = %q", got)
	}
}

func TestPrettyEvents(t *testing.T) {
	rep, buf := pretty()
	rep.Skip(2, 42, "/a.mkv", "already hevc")
	if got := buf.String(); got != "\x1b[2m– [2/42] /a.mkv — already hevc\x1b[0m\n" {
		t.Errorf("pretty skip = %q", got)
	}

	buf.Reset()
	rep.Start("Processing", 6, 42, "/e.mkv", "4.2G", 100*time.Second, "plan: CQ 26")
	want := "\x1b[36m▶ [6/42] \x1b[0m \x1b[1m/e.mkv\x1b[0m \x1b[2m(4.2G)\x1b[0m\n" +
		"\x1b[2m  plan: CQ 26\x1b[0m\n"
	if got := buf.String(); got != want {
		t.Errorf("pretty start = %q, want %q", got, want)
	}

	// progress rewrites the line in place
	buf.Reset()
	rep.Progress(50*time.Second, "1.2x")
	got := buf.String()
	if !strings.HasPrefix(got, "\r\x1b[K") || !strings.Contains(got, "50%") || !strings.Contains(got, "0:00:50/0:01:40") {
		t.Errorf("pretty progress = %q", got)
	}

	// success replaces the bar line
	buf.Reset()
	rep.Success("Converted", "/e.mkv", 1000, 500, true, 90*time.Second)
	if got := buf.String(); !strings.HasPrefix(got, "\r\x1b[K  \x1b[32m✓") || !strings.Contains(got, "−50%") || !strings.Contains(got, "1m30s") {
		t.Errorf("pretty success = %q", got)
	}

	// failure adds the detail line
	buf.Reset()
	rep.Fail("failed to convert /e.mkv (left untouched)", "Invalid data found")
	got = stripANSI(buf.String())
	if !strings.Contains(got, "✗ failed to convert /e.mkv (left untouched)") || !strings.Contains(got, "└─ Invalid data found") {
		t.Errorf("pretty fail = %q", got)
	}
}

// stripANSI removes color codes so assertions can match plain fragments
// across tint boundaries.
func stripANSI(s string) string {
	for {
		start := strings.IndexByte(s, '\x1b')
		if start < 0 {
			return s
		}
		end := strings.IndexByte(s[start:], 'm')
		if end < 0 {
			return s
		}
		s = s[:start] + s[start+end+1:]
	}
}

func TestPrettySummary(t *testing.T) {
	rep, buf := pretty()
	rep.Summary("Done.", "Converted", "skipped", Outcome{
		Done: 38, Skipped: 3, Failed: 1,
		BytesIn: 120 * 1024 * 1024 * 1024, BytesOut: 44 * 1024 * 1024 * 1024,
		Failures: []Failure{{Path: "/g.mkv", Msg: "failed to convert"}},
		Elapsed:  4353 * time.Second,
	})
	out := stripANSI(buf.String())
	for _, want := range []string{
		"✓ 38 converted",
		"120G", "44G",
		"(−64%)",
		"– 3 skipped",
		"✗ 1 failed",
		"✗ /g.mkv — failed to convert",
		"⏱ 1h 12m 33s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("pretty summary missing %q in:\n%s", want, out)
		}
	}
}
