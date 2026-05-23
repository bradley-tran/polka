package backend

import (
	"strings"
	"testing"
)

func TestSelectComposerReleaseVersionPrefersStablePatchForMinorLabel(t *testing.T) {
	page := strings.Join([]string{
		`<a href="https://getcomposer.org/download/2.10.0-RC2/composer.phar">rc</a>`,
		`<a href="https://getcomposer.org/download/2.9.8/composer.phar">stable</a>`,
		`<a href="https://getcomposer.org/download/2.8.12/composer.phar">minor-latest</a>`,
		`<a href="https://getcomposer.org/download/2.8.11/composer.phar">minor-older</a>`,
		`<a href="https://getcomposer.org/download/2.2.28/composer.phar">lts</a>`,
	}, "\n")

	version, err := selectComposerReleaseVersion(page, "2.8")
	if err != nil {
		t.Fatalf("selectComposerReleaseVersion(2.8) error = %v", err)
	}
	if version != "2.8.12" {
		t.Fatalf("selectComposerReleaseVersion(2.8) = %q, want %q", version, "2.8.12")
	}

	version, err = selectComposerReleaseVersion(page, "2")
	if err != nil {
		t.Fatalf("selectComposerReleaseVersion(2) error = %v", err)
	}
	if version != "2.9.8" {
		t.Fatalf("selectComposerReleaseVersion(2) = %q, want %q", version, "2.9.8")
	}
}

func TestParseChecksumValueAcceptsSha256sumFormat(t *testing.T) {
	expected := strings.Repeat("a", 64)
	value, err := parseChecksumValue(expected + "  composer.phar\n")
	if err != nil {
		t.Fatalf("parseChecksumValue(sha256sum) error = %v", err)
	}
	if value != expected {
		t.Fatalf("parseChecksumValue(sha256sum) = %q, want %q", value, expected)
	}
}
