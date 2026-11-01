package tools

import (
	"reflect"
	"strings"
	"testing"
)

// TestManifestVersionConstraintAllowsLabelAndResolved verifies partial version
// labels and fully resolved versions against [min, below) bounds.
func TestManifestVersionConstraintAllowsLabelAndResolved(t *testing.T) {
	constraint := manifestVersionConstraint{
		Min:   "4.0.4",
		Below: "5",
	}

	labelTests := []struct {
		label string
		want  bool
	}{
		{label: "", want: true},
		{label: "4", want: true},
		{label: "v4", want: true},
		{label: "4.0", want: true},
		{label: "4.0.4", want: true},
		{label: "4.3", want: true},
		{label: "4.0.3", want: false},
		{label: "3", want: false},
		{label: "3.13", want: false},
		{label: "5", want: false},
		{label: "5.0", want: false},
		{label: "5.1.0", want: false},
	}
	for _, test := range labelTests {
		if got := constraint.allowsLabel(test.label); got != test.want {
			t.Fatalf("allowsLabel(%q) = %v, want %v", test.label, got, test.want)
		}
	}

	resolvedTests := []struct {
		version string
		want    bool
	}{
		{version: "4.0.4", want: true},
		{version: "v4.3.2", want: true},
		{version: "4.99.99", want: true},
		{version: "4.0.3", want: false},
		{version: "4.0", want: false},
		{version: "3.13.7", want: false},
		{version: "5.0.0", want: false},
	}
	for _, test := range resolvedTests {
		if got := constraint.allowsResolved(test.version); got != test.want {
			t.Fatalf("allowsResolved(%q) = %v, want %v", test.version, got, test.want)
		}
	}

	err := constraint.error(RabbitMQ, "3.13")
	if err == nil || !strings.Contains(err.Error(), "requires >= 4.0.4 and < 5") {
		t.Fatalf("constraint.error() = %v, want range message", err)
	}
}

// TestDerivedTemplateValues verifies {major}, {minor}, and {major_minor}
// extraction from resolved version strings.
func TestDerivedTemplateValues(t *testing.T) {
	got := derivedTemplateValues("v8.4.9")
	want := map[string]string{
		"major":       "8",
		"minor":       "4",
		"major_minor": "8.4",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("derivedTemplateValues(v8.4.9) = %#v, want %#v", got, want)
	}

	single := derivedTemplateValues("24")
	if single["major"] != "24" || single["minor"] != "" || single["major_minor"] != "24" {
		t.Fatalf("derivedTemplateValues(24) = %#v, want major-only values", single)
	}
}

// TestWrapperScriptRendersWindowsAndPOSIX verifies the generated batch and
// POSIX shell wrapper scripts forward fixed subcommands and user arguments.
func TestWrapperScriptRendersWindowsAndPOSIX(t *testing.T) {
	win := wrapperScript("windows", "corepack.cmd", []string{"yarn"})
	wantWin := "@echo off\r\n" +
		"setlocal\r\n" +
		"call \"%~dp0corepack.cmd\" yarn %*\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
	if win != wantWin {
		t.Fatalf("wrapperScript(windows) = %q, want %q", win, wantWin)
	}

	posix := wrapperScript("linux", "frankenphp", []string{"php-cli"})
	wantPOSIX := "#!/bin/sh\n" +
		"set -eu\n" +
		"SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"exec \"$SCRIPT_DIR/frankenphp\" php-cli \"$@\"\n"
	if posix != wantPOSIX {
		t.Fatalf("wrapperScript(linux) = %q, want %q", posix, wantPOSIX)
	}
}
