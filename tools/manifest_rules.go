package tools

import (
	"fmt"
	"regexp"
	"strings"
)

// validWrapperArg restricts declarative wrapper arguments to safe literal
// tokens so generated shell and batch scripts never require escaping.
var validWrapperArg = regexp.MustCompile(`^[A-Za-z0-9._:=/@+-]+$`)

// manifestVersionConstraint declares an optional [min, below) version range
// for a tool manifest. Min is inclusive; Below is exclusive.
type manifestVersionConstraint struct {
	Min   string `yaml:"min"`
	Below string `yaml:"below"`
}

// manifestDependency declares a managed tool version that must be installed
// before the declaring tool when the declaring tool is configured.
type manifestDependency struct {
	Tool    string `yaml:"tool"`
	Version string `yaml:"version"`
}

// manifestWrapper declares a command wrapper script generated next to a
// resolved target executable during post-install.
type manifestWrapper struct {
	Target       manifestPlatformPaths `yaml:"target"`
	Args         []string              `yaml:"args"`
	SkipIfExists bool                  `yaml:"skip-if-exists"`
}

// isZero reports whether the version constraint declares neither bound.
func (c manifestVersionConstraint) isZero() bool {
	return strings.TrimSpace(c.Min) == "" && strings.TrimSpace(c.Below) == ""
}

// allowsLabel reports whether a possibly partial version label (such as "4" or
// "4.1") can still satisfy the constraint once resolved to a full version.
func (c manifestVersionConstraint) allowsLabel(label string) bool {
	normalized := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(label), "v"))
	if normalized == "" || c.isZero() {
		return true
	}

	if min := strings.TrimSpace(c.Min); min != "" {
		truncatedMin := truncateVersionParts(min, versionPartCount(normalized))
		if compareVersions(normalized, truncatedMin) < 0 {
			return false
		}
	}
	if below := strings.TrimSpace(c.Below); below != "" {
		if compareVersions(normalized, below) >= 0 {
			return false
		}
	}

	return true
}

// allowsResolved reports whether a fully resolved version satisfies the
// constraint bounds.
func (c manifestVersionConstraint) allowsResolved(version string) bool {
	normalized := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	if normalized == "" || c.isZero() {
		return true
	}

	if min := strings.TrimSpace(c.Min); min != "" && compareVersions(normalized, min) < 0 {
		return false
	}
	if below := strings.TrimSpace(c.Below); below != "" && compareVersions(normalized, below) >= 0 {
		return false
	}

	return true
}

// error formats the user-facing error when version violates the constraint.
func (c manifestVersionConstraint) error(tool, version string) error {
	min := strings.TrimSpace(c.Min)
	below := strings.TrimSpace(c.Below)
	switch {
	case min != "" && below != "":
		return fmt.Errorf("%s version %q is unsupported: requires >= %s and < %s", tool, version, min, below)
	case min != "":
		return fmt.Errorf("%s version %q is unsupported: requires >= %s", tool, version, min)
	case below != "":
		return fmt.Errorf("%s version %q is unsupported: requires < %s", tool, version, below)
	default:
		return nil
	}
}

func versionPartCount(version string) int {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return 0
	}

	return strings.Count(trimmed, ".") + 1
}

func truncateVersionParts(version string, count int) string {
	trimmed := strings.TrimSpace(version)
	if count <= 0 || trimmed == "" {
		return trimmed
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) <= count {
		return trimmed
	}

	return strings.Join(parts[:count], ".")
}

// derivedTemplateValues computes the built-in {major}, {minor}, and
// {major_minor} template variables from a resolved version string.
func derivedTemplateValues(version string) map[string]string {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(version), "v"))
	values := map[string]string{
		"major":       "",
		"minor":       "",
		"major_minor": versionMajorMinor(trimmed),
	}
	if trimmed == "" {
		return values
	}

	parts := strings.Split(trimmed, ".")
	if len(parts) >= 1 {
		values["major"] = parts[0]
	}
	if len(parts) >= 2 {
		values["minor"] = parts[1]
	}

	return values
}

// wrapperScript renders the platform-specific wrapper script that invokes
// targetFile (located in the same directory as the wrapper) with args followed
// by all forwarded CLI arguments.
func wrapperScript(goos, targetFile string, args []string) string {
	argSuffix := ""
	if len(args) > 0 {
		argSuffix = " " + strings.Join(args, " ")
	}

	if strings.TrimSpace(goos) == "windows" {
		return "@echo off\r\n" +
			"setlocal\r\n" +
			"call \"%~dp0" + targetFile + "\"" + argSuffix + " %*\r\n" +
			"exit /b %ERRORLEVEL%\r\n"
	}

	return "#!/bin/sh\n" +
		"set -eu\n" +
		"SCRIPT_DIR=$(CDPATH= cd -- \"$(dirname -- \"$0\")\" && pwd)\n" +
		"exec \"$SCRIPT_DIR/" + targetFile + "\"" + argSuffix + " \"$@\"\n"
}
