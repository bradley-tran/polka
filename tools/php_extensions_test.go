package tools

import (
	"strings"
	"testing"
)

func TestRenderPHPConfigCombinesExtensionsAndOPcache(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		Extensions: map[string]bool{
			"zip":     true,
			"openssl": false,
		},
		OPcacheConfig: map[string]string{
			"OPcache.Validate_Timestamps": "1",
			"opcache.enable":              "1",
		},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	for _, want := range []string{
		"extension_dir=\"../ext\"",
		";extension=openssl",
		"extension=zip",
		"[opcache]",
		"opcache.enable=1",
		"opcache.validate_timestamps=1",
	} {
		if !strings.Contains(phpIni, want) {
			t.Fatalf("php.ini = %q, want %s", phpIni, want)
		}
	}
	if strings.Index(phpIni, ";extension=openssl") > strings.Index(phpIni, "extension=zip") {
		t.Fatalf("php.ini = %q, want sorted extension entries", phpIni)
	}
	if strings.Index(phpIni, "opcache.enable=1") > strings.Index(phpIni, "opcache.validate_timestamps=1") {
		t.Fatalf("php.ini = %q, want sorted OPcache entries", phpIni)
	}
}

func TestRenderPHPConfigSupportsOPcacheOnly(t *testing.T) {
	configData, err := renderPHPConfig("../ext", PHPInstallConfig{
		OPcacheConfig: map[string]string{
			"opcache.enable": "1",
		},
	})
	if err != nil {
		t.Fatalf("renderPHPConfig() error = %v", err)
	}

	phpIni := string(configData)
	if strings.Contains(phpIni, "extension_dir") {
		t.Fatalf("php.ini = %q, want no extension_dir for OPcache-only config", phpIni)
	}
	if !strings.Contains(phpIni, "[opcache]\nopcache.enable=1\n") {
		t.Fatalf("php.ini = %q, want OPcache section", phpIni)
	}
}
