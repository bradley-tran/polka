package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestResolvePHPBuildTools verifies Linux host-tool discovery accepts a
// matching php-dev toolset and standard C build commands.
func TestResolvePHPBuildTools(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("host PHP build-tool verification is Linux-specific")
	}
	binDir := t.TempDir()
	scripts := map[string]string{
		"php":        "#!/bin/sh\nprintf '8.4.24~8.4~0~x64\\n'\n",
		"phpize":     "#!/bin/sh\nexit 0\n",
		"php-config": "#!/bin/sh\nprintf '8.4.24\\n'\n",
		"make":       "#!/bin/sh\nexit 0\n",
		"autoconf":   "#!/bin/sh\nexit 0\n",
		"cc":         "#!/bin/sh\nexit 0\n",
	}
	for name, content := range scripts {
		path := filepath.Join(binDir, name)
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	t.Setenv("PATH", binDir)

	phpPath := filepath.Join(binDir, "php")
	phpize, phpConfig, err := resolvePHPBuildTools(binDir, phpPath, "PHP extension SDK")
	if err != nil {
		t.Fatalf("resolvePHPBuildTools() error = %v", err)
	}
	if phpize != filepath.Join(binDir, "phpize") || phpConfig != filepath.Join(binDir, "php-config") {
		t.Fatalf("resolvePHPBuildTools() = %q, %q; want project host tools", phpize, phpConfig)
	}
}
