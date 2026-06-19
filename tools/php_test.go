package tools

import (
	"strings"
	"testing"
)

func TestSelectPHPWindowsAssetUsesStrictFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"nts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-nts.zip"}},
			"ts-vs17-x64":  {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	nts, err := selectPHPWindowsAsset(release, false)
	if err != nil || nts.Path != "php-nts.zip" {
		t.Fatalf("selectPHPWindowsAsset(NTS) = %#v, %v", nts, err)
	}
	zts, err := selectPHPWindowsAsset(release, true)
	if err != nil || zts.Path != "php-ts.zip" {
		t.Fatalf("selectPHPWindowsAsset(ZTS) = %#v, %v", zts, err)
	}
}

func TestSelectPHPWindowsAssetDoesNotCrossFlavor(t *testing.T) {
	release := phpWindowsRelease{
		Version: "8.4.8",
		Variants: map[string]phpWindowsVariant{
			"ts-vs17-x64": {Zip: phpWindowsAsset{Path: "php-ts.zip"}},
		},
	}

	_, err := selectPHPWindowsAsset(release, false)
	if err == nil || !strings.Contains(err.Error(), "NTS") {
		t.Fatalf("selectPHPWindowsAsset(NTS) error = %v, want strict NTS failure", err)
	}
}
