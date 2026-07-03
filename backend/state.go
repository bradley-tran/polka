package backend

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// installStateFileName is the project-local manifest of successfully installed
// tools, stored inside the envs directory so that removing the directory also
// resets the recorded state.
const installStateFileName = "installed.json"

// installStateVersion is the current schema version of the install state file.
const installStateVersion = 1

// installState records which tool versions have been fully installed (payload
// extracted and post-install hooks completed) into the project envs directory.
// It is the minimal state used by Install to skip unchanged tools.
type installState struct {
	Version int                 `json:"version"`
	Tools   map[string][]string `json:"tools,omitempty"`
}

// has reports whether the given tool version is recorded as installed.
func (s installState) has(tool, version string) bool {
	for _, installed := range s.Tools[tool] {
		if installed == version {
			return true
		}
	}

	return false
}

// record marks the given tool version as installed, deduplicating entries and
// keeping versions sorted for stable file output.
func (s *installState) record(tool, version string) {
	if s.has(tool, version) {
		return
	}
	if s.Tools == nil {
		s.Tools = map[string][]string{}
	}
	s.Tools[tool] = append(s.Tools[tool], version)
	sort.Strings(s.Tools[tool])
}

// installStateFile returns the path of the project-local install state file.
func (s Store) installStateFile() string {
	return filepath.Join(s.EnvsDir, installStateFileName)
}

// readInstallState loads the recorded install state. A missing or unreadable
// file yields an empty state so installs fall back to installing everything.
func (s Store) readInstallState() installState {
	data, err := os.ReadFile(s.installStateFile())
	if err != nil {
		return installState{Version: installStateVersion}
	}

	var state installState
	if err := json.Unmarshal(data, &state); err != nil || state.Version != installStateVersion {
		return installState{Version: installStateVersion}
	}

	return state
}

// writeInstallState persists the install state atomically via a temporary file
// rename so a crash cannot leave a truncated state file behind.
func (s Store) writeInstallState(state installState) error {
	if err := os.MkdirAll(s.EnvsDir, 0o755); err != nil {
		return fmt.Errorf("create envs directory: %w", err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode install state: %w", err)
	}

	tempFile, err := os.CreateTemp(s.EnvsDir, installStateFileName+"-tmp-")
	if err != nil {
		return fmt.Errorf("create install state temp file: %w", err)
	}
	tempPath := tempFile.Name()
	if _, err := tempFile.Write(append(data, '\n')); err != nil {
		_ = tempFile.Close()
		_ = os.Remove(tempPath)
		return fmt.Errorf("write install state: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("close install state temp file: %w", err)
	}
	if err := os.Rename(tempPath, s.installStateFile()); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("finalize install state file: %w", err)
	}

	return nil
}
