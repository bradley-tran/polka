package backend

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"polka/tools"
)

// transferTestPlugin is a tool whose download reports byte counts through the same
// context field that the manifest download path uses.
type transferTestPlugin struct {
	fakeAcmePlugin
	t *testing.T
}

// InstallCandidates points at the single-file payload inside the versioned install
// directory, so the install step can find what the download cached.
func (transferTestPlugin) InstallCandidates(root, version string) []string {
	return []string{filepath.Join(root, "acme", version, "acme")}
}

// Download reports two progress events and then fills the cache entry.
func (p transferTestPlugin) Download(ctx ToolDownloadContext) error {
	if ctx.Progress == nil {
		return errors.New("download context has no progress callback")
	}
	ctx.Progress(tools.DownloadProgress{Downloaded: 10, Total: 40})
	ctx.Progress(tools.DownloadProgress{Downloaded: 40, Total: 40})
	writeCachedFilePayload(p.t, ctx.CacheDir, ctx.Tool, ctx.Version, "acme", "acme", []byte("acme\n"))
	return nil
}

// TestInstallRequestLayerReportsTransferEvents verifies that byte counts from the default
// downloader reach the report callback as their own stage, keeping row identity and
// leaving the regular stage events in place.
func TestInstallRequestLayerReportsTransferEvents(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	registry, err := NewToolRegistry(transferTestPlugin{t: t})
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	store.Plugins = registry
	if err := os.MkdirAll(store.EnvsDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(envs) error = %v", err)
	}

	var transfers []InstallProgress
	var stages []InstallProgressStage
	report := func(progress InstallProgress) {
		if progress.Stage == InstallProgressTransfer {
			transfers = append(transfers, progress)
			return
		}
		stages = append(stages, progress.Stage)
	}
	requests := []tools.InstallRequest{{Tool: "acme", Version: "1.0"}}
	if _, err := store.installRequestLayer(Environment{}, requests, false, report, 2, 5); err != nil {
		t.Fatalf("installRequestLayer() error = %v", err)
	}

	if len(transfers) != 2 {
		t.Fatalf("transfer events = %+v, want 2", transfers)
	}
	want := InstallProgress{
		Index:         3,
		Total:         5,
		Tool:          "acme",
		Version:       "1.0",
		Stage:         InstallProgressTransfer,
		Downloaded:    40,
		DownloadTotal: 40,
	}
	if transfers[1] != want {
		t.Fatalf("last transfer event = %+v, want %+v", transfers[1], want)
	}

	wantStages := []InstallProgressStage{InstallProgressDownloading, InstallProgressInstalling, InstallProgressInstalled}
	if len(stages) != len(wantStages) {
		t.Fatalf("stages = %v, want %v", stages, wantStages)
	}
	for i := range wantStages {
		if stages[i] != wantStages[i] {
			t.Fatalf("stages = %v, want %v", stages, wantStages)
		}
	}
}

// TestEnsureCachedToolWithoutTransferCallback verifies that internal tool provisioning,
// which passes no callback, downloads normally and leaves progress unset.
func TestEnsureCachedToolWithoutTransferCallback(t *testing.T) {
	projectDir := t.TempDir()
	store := NewProjectStore(projectDir)
	store.CacheDir = filepath.Join(projectDir, "global-cache")
	var progressWasSet bool
	store.Plugins = mustToolRegistry(t, progressProbePlugin{t: t, progressWasSet: &progressWasSet})

	if _, downloaded, err := store.ensureCachedTool("acme", "1.0", nil, nil); err != nil {
		t.Fatalf("ensureCachedTool() error = %v", err)
	} else if !downloaded {
		t.Fatal("ensureCachedTool() downloaded = false, want true")
	}
	if progressWasSet {
		t.Fatal("download context has a progress callback, want nil")
	}
}

// progressProbePlugin records whether its download received a progress callback.
type progressProbePlugin struct {
	fakeAcmePlugin
	t              *testing.T
	progressWasSet *bool
}

// Download records the callback's presence and then fills the cache entry.
func (p progressProbePlugin) Download(ctx ToolDownloadContext) error {
	*p.progressWasSet = ctx.Progress != nil
	writeCachedFilePayload(p.t, ctx.CacheDir, ctx.Tool, ctx.Version, "acme", "acme", []byte("acme\n"))
	return nil
}

// mustToolRegistry builds a registry containing only the given plugins.
func mustToolRegistry(t *testing.T, plugins ...ToolPlugin) *ToolRegistry {
	t.Helper()
	registry, err := NewToolRegistry(plugins...)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	return registry
}
