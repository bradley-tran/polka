package cli

import (
	"bytes"
	"strings"
	"testing"

	"polka/backend"
)

// newTestSpinner returns a spinner with one registered row. The buffer is not a
// terminal, so tests needing TTY behaviour set tty directly and call Report without
// Start, leaving rendering to explicit renderLine calls.
func newTestSpinner(out *bytes.Buffer, tty bool) *installSpinner {
	spinner := newInstallSpinner(out, 1)
	spinner.Register("php", "8.4.1")
	spinner.tty = tty
	return spinner
}

// transferEvent builds a byte-count event for the test spinner's row.
func transferEvent(downloaded, total int64) backend.InstallProgress {
	return backend.InstallProgress{
		Tool:          "php",
		Version:       "8.4.1",
		Stage:         backend.InstallProgressTransfer,
		Downloaded:    downloaded,
		DownloadTotal: total,
	}
}

// stageEvent builds a stage event for the test spinner's row.
func stageEvent(stage backend.InstallProgressStage) backend.InstallProgress {
	return backend.InstallProgress{Tool: "php", Version: "8.4.1", Stage: stage}
}

// TestFormatDownloadProgress covers both row formats and the unit selection.
func TestFormatDownloadProgress(t *testing.T) {
	cases := []struct {
		name              string
		downloaded, total int64
		want              string
	}{
		{"not started", 0, 43_000_000, ""},
		{"known total", 18_000_000, 43_000_000, "  41% 18/43 MB"},
		{"finished", 43_000_000, 43_000_000, "  100% 43/43 MB"},
		{"small file uses KB", 120_000, 480_000, "  25% 120/480 KB"},
		{"unknown total", 18_000_000, 0, "  18 MB"},
		{"unknown total small", 512_000, 0, "  512 KB"},
		{"overshoot is clamped", 50_000_000, 43_000_000, "  100% 50/43 MB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatDownloadProgress(tc.downloaded, tc.total); got != tc.want {
				t.Fatalf("formatDownloadProgress(%d, %d) = %q, want %q", tc.downloaded, tc.total, got, tc.want)
			}
		})
	}
}

// TestSpinnerRowShowsTransferOnlyWhileDownloading verifies the suffix appears during
// the downloading stage and disappears once the tool moves on.
func TestSpinnerRowShowsTransferOnlyWhileDownloading(t *testing.T) {
	var out bytes.Buffer
	spinner := newTestSpinner(&out, true)

	spinner.Report(stageEvent(backend.InstallProgressDownloading))
	spinner.Report(transferEvent(18_000_000, 43_000_000))
	if got, want := spinner.renderLine(spinner.tools[0]), "↓ php 8.4.1  41% 18/43 MB"; got != want {
		t.Fatalf("downloading row = %q, want %q", got, want)
	}

	spinner.Report(stageEvent(backend.InstallProgressInstalling))
	got := spinner.renderLine(spinner.tools[0])
	if strings.Contains(got, "MB") || strings.Contains(got, "%") {
		t.Fatalf("installing row = %q, want no transfer suffix", got)
	}
}

// TestSpinnerTransferDoesNotChangeStage verifies that a late transfer event cannot pull
// a row back to a previous stage.
func TestSpinnerTransferDoesNotChangeStage(t *testing.T) {
	var out bytes.Buffer
	spinner := newTestSpinner(&out, true)

	spinner.Report(stageEvent(backend.InstallProgressInstalling))
	spinner.Report(transferEvent(1_000_000, 2_000_000))
	if got := spinner.tools[0].stage; got != backend.InstallProgressInstalling {
		t.Fatalf("stage = %q, want %q", got, backend.InstallProgressInstalling)
	}
}

// TestSpinnerUnregisteredTransferIsIgnored verifies that an event for a row that does
// not exist neither panics nor changes the registered one.
func TestSpinnerUnregisteredTransferIsIgnored(t *testing.T) {
	var out bytes.Buffer
	spinner := newTestSpinner(&out, true)

	spinner.Report(backend.InstallProgress{
		Tool:          "erlang",
		Version:       "27",
		Stage:         backend.InstallProgressTransfer,
		Downloaded:    10,
		DownloadTotal: 20,
	})
	if got := spinner.tools[0].downloaded; got != 0 {
		t.Fatalf("registered row downloaded = %d, want 0", got)
	}
}

// TestSpinnerNonTTYOutputIsStageLinesOnly verifies piped output: exactly one line per
// stage, with byte counts never reaching it.
func TestSpinnerNonTTYOutputIsStageLinesOnly(t *testing.T) {
	var out bytes.Buffer
	spinner := newTestSpinner(&out, false)

	spinner.Report(stageEvent(backend.InstallProgressDownloading))
	for downloaded := int64(1); downloaded <= 5; downloaded++ {
		spinner.Report(transferEvent(downloaded*1_000_000, 5_000_000))
	}
	spinner.Report(stageEvent(backend.InstallProgressInstalled))

	want := "php 8.4.1: downloading\nphp 8.4.1: installed\n"
	if got := out.String(); got != want {
		t.Fatalf("non-TTY output = %q, want %q", got, want)
	}
}
