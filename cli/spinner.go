package cli

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"polka/backend"
)

// spinnerFrames are the braille-dot animation frames used while a tool is in progress.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const installSpinnerInterval = 200 * time.Millisecond

// isTTY reports whether w is an *os.File connected to a terminal.
func isTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// stdinIsTTY reports whether standard input is connected to a terminal.
// Interactive TUI commands require both a terminal stdout (for rendering)
// and a terminal stdin (for key input).
func stdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

// toolState holds the display state for a single tool being installed.
type toolState struct {
	tool    string
	version string
	stage   backend.InstallProgressStage
	done    bool // true once "installed" is received
	// downloaded and downloadTotal hold the latest transfer counts for the row.
	downloaded    int64
	downloadTotal int64
}

// installSpinner renders a live spinner list of tools during a concurrent install.
// On non-TTY writers it falls back to plain line-by-line output.
type installSpinner struct {
	mu     sync.Mutex
	out    io.Writer
	tty    bool
	tools  []toolState    // ordered slot per install request
	keys   map[string]int // "tool@version" → tools index
	lines  []string
	frame  int
	ticker *time.Ticker
	done   chan struct{}
}

func newInstallSpinner(out io.Writer, requests int) *installSpinner {
	s := &installSpinner{
		out:   out,
		tty:   isTTY(out),
		tools: make([]toolState, 0, requests),
		keys:  make(map[string]int, requests),
	}
	return s
}

// key returns a stable map key for a tool+version pair.
func toolKey(tool, version string) string {
	return tool + "@" + version
}

// Register allocates a slot for a tool before installation starts.
// Must be called from a single goroutine before Start().
func (s *installSpinner) Register(tool, version string) {
	k := toolKey(tool, version)
	if _, ok := s.keys[k]; !ok {
		s.keys[k] = len(s.tools)
		s.tools = append(s.tools, toolState{tool: tool, version: version})
	}
}

// Start begins the spinner animation loop (TTY only).
func (s *installSpinner) Start() {
	if !s.tty {
		return
	}
	s.ticker = time.NewTicker(installSpinnerInterval)
	s.done = make(chan struct{})
	s.hideCursor()
	s.mu.Lock()
	s.render()
	s.mu.Unlock()
	go func() {
		for {
			select {
			case <-s.ticker.C:
				s.mu.Lock()
				s.frame = (s.frame + 1) % len(spinnerFrames)
				s.render()
				s.mu.Unlock()
			case <-s.done:
				return
			}
		}
	}()
}

// Update records a new stage for a tool. Safe to call from any goroutine.
func (s *installSpinner) Update(tool, version string, stage backend.InstallProgressStage) {
	k := toolKey(tool, version)
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.keys[k]
	if !ok {
		return
	}
	s.tools[idx].stage = stage
	if stage == backend.InstallProgressInstalled {
		s.tools[idx].done = true
	}

	if s.tty {
		s.render()
		return
	}

	// Non-TTY: emit a plain line immediately.
	t := s.tools[idx]
	_, _ = fmt.Fprintf(s.out, "%s %s: %s\n", t.tool, t.version, stage)
}

// Report routes one install progress event. Safe to call from any goroutine.
func (s *installSpinner) Report(progress backend.InstallProgress) {
	if progress.Stage == backend.InstallProgressTransfer {
		s.updateTransfer(progress)
		return
	}
	s.Update(progress.Tool, progress.Version, progress.Stage)
}

// updateTransfer stores byte counts for the next animation tick to render. Without
// a terminal there is no tick and nothing to redraw, so piped output stays limited
// to one line per stage.
func (s *installSpinner) updateTransfer(progress backend.InstallProgress) {
	if !s.tty {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, ok := s.keys[toolKey(progress.Tool, progress.Version)]
	if !ok {
		return
	}
	s.tools[idx].downloaded = progress.Downloaded
	s.tools[idx].downloadTotal = progress.DownloadTotal
}

// Stop terminates the animation and prints the final state (TTY only).
func (s *installSpinner) Stop() {
	if !s.tty {
		return
	}
	s.ticker.Stop()
	close(s.done)
	// Small pause so the final render is clean.
	time.Sleep(20 * time.Millisecond)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.renderFinal()
	s.showCursor()
}

// hideCursor keeps the terminal insertion cursor from competing with the spinner.
func (s *installSpinner) hideCursor() {
	fmt.Fprint(s.out, "\033[?25l")
}

// showCursor restores the terminal insertion cursor after the spinner exits.
func (s *installSpinner) showCursor() {
	fmt.Fprint(s.out, "\033[?25h")
}

// render refreshes changed tool lines in-place. Must be called with mu held.
func (s *installSpinner) render() {
	n := len(s.tools)
	if n == 0 {
		return
	}
	if len(s.lines) != n {
		s.lines = make([]string, n)
	}

	for i, t := range s.tools {
		line := s.renderLine(t)
		if s.lines[i] == line {
			continue
		}
		s.replaceLine(i, line)
		s.lines[i] = line
	}
}

// renderLine formats one tool row for the current spinner frame.
func (s *installSpinner) renderLine(t toolState) string {
	icon := spinnerFrames[s.frame]
	if t.done {
		icon = "✓"
	} else if t.stage == backend.InstallProgressDownloading {
		// A large download can take minutes, so show that the row is moving.
		return fmt.Sprintf("↓ %s %s%s", t.tool, t.version, formatDownloadProgress(t.downloaded, t.downloadTotal))
	} else if t.stage == backend.InstallProgressBuilding {
		// A compile can take many minutes, so say why this row is not moving.
		return fmt.Sprintf("%s %s %s (building)", icon, t.tool, t.version)
	}
	return fmt.Sprintf("%s %s %s", icon, t.tool, t.version)
}

// formatDownloadProgress renders the suffix of a downloading row, such as
// "  42% 18/43 MB", or "  18 MB" when the size is unknown. It stays short because a
// wrapped row would break in-place rendering.
func formatDownloadProgress(downloaded, total int64) string {
	if downloaded <= 0 {
		return ""
	}
	if total <= 0 {
		divisor, unit := downloadSizeUnit(downloaded)
		return fmt.Sprintf("  %d %s", downloaded/divisor, unit)
	}
	divisor, unit := downloadSizeUnit(total)
	percent := min(downloaded*100/total, 100)
	return fmt.Sprintf("  %d%% %d/%d %s", percent, downloaded/divisor, total/divisor, unit)
}

// downloadSizeUnit picks a unit for a byte count, so that a small file reads
// "120/480 KB" rather than "0/0 MB".
func downloadSizeUnit(size int64) (int64, string) {
	if size < 1_000_000 {
		return 1_000, "KB"
	}
	return 1_000_000, "MB"
}

// replaceLine overwrites one rendered row and returns the cursor to the bottom.
func (s *installSpinner) replaceLine(index int, line string) {
	up := len(s.tools) - index
	if up > 0 {
		fmt.Fprintf(s.out, "\033[%dA", up)
	}
	fmt.Fprintf(s.out, "\033[2K%s\n", line)
	down := len(s.tools) - index - 1
	if down > 0 {
		fmt.Fprintf(s.out, "\033[%dB", down)
	}
}

// renderFinal prints a clean final state (all ✓) without spinner frames.
// Must be called with mu held.
func (s *installSpinner) renderFinal() {
	n := len(s.tools)
	if n == 0 {
		return
	}
	if len(s.lines) != n {
		s.lines = make([]string, n)
	}
	for i, t := range s.tools {
		line := fmt.Sprintf("✓ %s %s", t.tool, t.version)
		if s.lines[i] == line {
			continue
		}
		s.replaceLine(i, line)
		s.lines[i] = line
	}
}

// printInitialLines emits blank placeholder lines so render() can move up into them.
// Must be called once, before Start(), with no concurrent writers.
func (s *installSpinner) printInitialLines() {
	if !s.tty {
		return
	}
	for range s.tools {
		fmt.Fprintln(s.out)
	}
}
