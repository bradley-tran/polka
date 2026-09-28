package backend

import (
	"bytes"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"polka/config"
	"polka/tools"
)

const (
	// msvcStateDirName holds the captured MSVC developer environment per
	// Polka environment under .polka/envs, so deleting envs resets it too.
	msvcStateDirName = "msvc"
	// msvcTargetArch is the vcvarsall target; the PHP SDK is x64-only.
	msvcTargetArch = "x64"
	// msvcVCToolsComponent is the Visual Studio component that provides
	// cl.exe, link.exe, dumpbin.exe and friends for x86/x64 targets.
	msvcVCToolsComponent = "Microsoft.VisualStudio.Component.VC.Tools.x86.x64"
	// msvcCaptureMarker separates the baseline and vcvarsall environment dumps.
	msvcCaptureMarker = "::POLKA-MSVC-CAPTURE::"
)

// msvcListVariables are the semicolon-separated variables vcvarsall extends;
// only the entries it adds are stored so the user's own values still apply.
var msvcListVariables = map[string]struct{}{
	"PATH":             {},
	"INCLUDE":          {},
	"LIB":              {},
	"LIBPATH":          {},
	"EXTERNAL_INCLUDE": {},
}

// msvcIgnoredVariables are cmd.exe or vcvarsall bookkeeping variables that
// must not leak into project shells.
var msvcIgnoredVariables = map[string]struct{}{
	"PROMPT":            {},
	"POLKA_VCVARSALL":   {},
	"POLKA_VCVARS_ARCH": {},
	"POLKA_VCVARS_ARGS": {},
}

// MSVCToolchain is the Visual Studio developer environment captured by
// `polka install` so `polka sh` and `polka exec` can run cl.exe, link.exe,
// nmake.exe and dumpbin.exe without a Developer Command Prompt.
type MSVCToolchain struct {
	// DisplayName is the Visual Studio product name, e.g. "Visual Studio
	// Build Tools 2022".
	DisplayName string `json:"display-name"`
	// Version is the user-facing product version, e.g. "17.14.2".
	Version string `json:"version"`
	// InstallationPath is the Visual Studio instance root.
	InstallationPath string `json:"installation-path"`
	// InstallationVersion is vswhere's full build version, used to detect
	// Visual Studio updates that move toolset directories.
	InstallationVersion string `json:"installation-version"`
	// VCVarsAll is the script the environment was captured from.
	VCVarsAll string `json:"vcvarsall"`
	// Arch is the vcvarsall target architecture argument.
	Arch string `json:"arch"`
	// Set holds variables vcvarsall defines outright.
	Set map[string]string `json:"set,omitempty"`
	// Prepend holds entries vcvarsall adds in front of list variables such as
	// PATH, INCLUDE and LIB.
	Prepend map[string][]string `json:"prepend,omitempty"`
}

// msvcInstance is the subset of vswhere's JSON output Polka uses to choose a
// Visual Studio installation.
type msvcInstance struct {
	InstallationPath    string `json:"installationPath"`
	InstallationVersion string `json:"installationVersion"`
	DisplayName         string `json:"displayName"`
	IsPrerelease        bool   `json:"isPrerelease"`
	Catalog             struct {
		ProductDisplayVersion string `json:"productDisplayVersion"`
	} `json:"catalog"`
}

// Test hooks for host Visual Studio discovery; tests replace them so they
// never depend on the machine's Visual Studio installation.
var (
	msvcListInstances    = listMSVCInstances
	msvcCaptureVCVars    = captureMSVCVCVars
	msvcDetectPHPMajor   = detectPHPCompilerMajor
	msvcDetectPHPToolset = detectPHPToolset
	msvcVSWhereLocation  = defaultVSWhereLocations
)

// MSVCToolchain returns the MSVC developer environment captured for the
// environment by `polka install`, or nil when none was recorded or the
// environment does not enable the PHP extension SDK on Windows.
func (s Store) MSVCToolchain(environment Environment) (*MSVCToolchain, error) {
	if !environment.PHPBuildTools || runtime.GOOS != "windows" {
		return nil, nil
	}

	return readMSVCToolchain(s.msvcStateFile(environment.Name))
}

// msvcStateFile returns the per-environment captured toolchain path.
func (s Store) msvcStateFile(environmentName string) string {
	return filepath.Join(s.EnvsDir, msvcStateDirName, environmentName+".json")
}

// syncMSVCToolchain locates a host Visual Studio C++ toolchain and records
// its developer environment for the environment. A missing toolchain is not
// fatal because MSVC is a host prerequisite only needed for native builds;
// the reason is reported through warn and any stale capture is removed.
func (s Store) syncMSVCToolchain(environment Environment, force bool, warn func(string)) error {
	if !environment.PHPBuildTools || runtime.GOOS != "windows" {
		return nil
	}
	statePath := s.msvcStateFile(environment.Name)

	toolchain, err := s.detectMSVCToolchain(environment, statePath, force)
	if err != nil {
		if removeErr := os.Remove(statePath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return fmt.Errorf("remove stale MSVC environment %s: %w", statePath, removeErr)
		}
		if warn != nil {
			warn(fmt.Sprintf("%v; cl.exe and other MSVC tools will not be available in polka sh/exec. Install Visual Studio or Build Tools with the \"Desktop development with C++\" workload, then re-run polka install", err))
		}
		return nil
	}

	return writeMSVCToolchain(statePath, toolchain)
}

// detectMSVCToolchain selects the Visual Studio instance matching the PHP
// runtime's compiler when available, then captures its vcvarsall environment
// unless an up-to-date capture for the same instance already exists.
func (s Store) detectMSVCToolchain(environment Environment, statePath string, force bool) (MSVCToolchain, error) {
	vswhere, err := s.findVSWhere()
	if err != nil {
		return MSVCToolchain{}, err
	}
	instances, err := msvcListInstances(vswhere)
	if err != nil {
		return MSVCToolchain{}, err
	}

	preferredMajor := 0
	targetToolset := ""
	tool, version := config.PrimaryPHPTool(environment)
	if tool != "" && version != "" {
		if phpPath, resolveErr := s.resolveInstalledTool(tool, version); resolveErr == nil {
			preferredMajor = msvcDetectPHPMajor(phpPath)
			targetToolset = msvcDetectPHPToolset(phpPath)
		}
	}
	instance, ok := selectMSVCInstance(instances, preferredMajor)
	if !ok {
		return MSVCToolchain{}, fmt.Errorf("no Visual Studio installation with the MSVC x64 C++ build tools was found")
	}

	vcvarsall := filepath.Join(instance.InstallationPath, "VC", "Auxiliary", "Build", "vcvarsall.bat")
	if !regularFileExistsAt(vcvarsall) {
		return MSVCToolchain{}, fmt.Errorf("Visual Studio at %s has no %s", instance.InstallationPath, vcvarsall)
	}

	selectedToolset := selectMSVCToolset(instance.InstallationPath, targetToolset)
	vcvarsArgs := ""
	if selectedToolset != "" {
		vcvarsArgs = "-vcvars_ver=" + selectedToolset
	}

	if !force {
		existing, readErr := readMSVCToolchain(statePath)
		if readErr == nil && existing != nil &&
			strings.EqualFold(existing.InstallationPath, instance.InstallationPath) &&
			existing.InstallationVersion == instance.InstallationVersion &&
			existing.Arch == msvcTargetArch &&
			(selectedToolset == "" || strings.HasPrefix(existing.Set["VCToolsVersion"], selectedToolset)) {
			return *existing, nil
		}
	}

	baseline, captured, err := msvcCaptureVCVars(vcvarsall, msvcTargetArch, vcvarsArgs)
	if err != nil {
		return MSVCToolchain{}, err
	}
	set, prepend := diffMSVCEnvironment(baseline, captured)
	if _, ok := set["VCToolsInstallDir"]; !ok {
		return MSVCToolchain{}, fmt.Errorf("%s %s did not configure the MSVC toolchain", vcvarsall, msvcTargetArch)
	}

	displayVersion := strings.TrimSpace(instance.Catalog.ProductDisplayVersion)
	if displayVersion == "" {
		displayVersion = instance.InstallationVersion
	}

	return MSVCToolchain{
		DisplayName:         instance.DisplayName,
		Version:             displayVersion,
		InstallationPath:    instance.InstallationPath,
		InstallationVersion: instance.InstallationVersion,
		VCVarsAll:           vcvarsall,
		Arch:                msvcTargetArch,
		Set:                 set,
		Prepend:             prepend,
	}, nil
}

// findVSWhere returns the Visual Studio Installer's vswhere.exe, falling back
// to the copy bundled with the internal PHP SDK.
func (s Store) findVSWhere() (string, error) {
	candidates := msvcVSWhereLocation()
	candidates = append(candidates, filepath.Join(s.EnvsDir, toolPHPSDK, tools.DefaultPHPSDKVersion, "bin", "vswhere.exe"))
	for _, candidate := range candidates {
		if regularFileExistsAt(candidate) {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("vswhere.exe was not found, so no Visual Studio installation could be located")
}

// defaultVSWhereLocations returns the fixed path the Visual Studio Installer
// uses for vswhere.exe since Visual Studio 2017 15.2.
func defaultVSWhereLocations() []string {
	programFiles := strings.TrimSpace(os.Getenv("ProgramFiles(x86)"))
	if programFiles == "" {
		programFiles = `C:\Program Files (x86)`
	}

	return []string{filepath.Join(programFiles, "Microsoft Visual Studio", "Installer", "vswhere.exe")}
}

// listMSVCInstances asks vswhere for every Visual Studio product, including
// Build Tools and previews, that has the x64 C++ compiler installed.
func listMSVCInstances(vswhere string) ([]msvcInstance, error) {
	command := exec.Command(vswhere, "-products", "*", "-prerelease", "-requires", msvcVCToolsComponent, "-format", "json", "-utf8", "-nologo")
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("run %s: %w: %s", vswhere, err, strings.TrimSpace(stderr.String()))
	}

	return parseMSVCInstances(output)
}

// parseMSVCInstances decodes vswhere's JSON instance list.
func parseMSVCInstances(data []byte) ([]msvcInstance, error) {
	var instances []msvcInstance
	if err := json.Unmarshal(bytes.TrimSpace(data), &instances); err != nil {
		return nil, fmt.Errorf("parse vswhere output: %w", err)
	}

	return instances, nil
}

// selectMSVCInstance prefers stable releases over previews, then the Visual
// Studio major version PHP was built with, then the newest version. MSVC
// 2015 and later share a binary-compatible ABI, so a newer toolchain is an
// acceptable fallback when the matching one is not installed.
func selectMSVCInstance(instances []msvcInstance, preferredMajor int) (msvcInstance, bool) {
	candidates := make([]msvcInstance, 0, len(instances))
	for _, instance := range instances {
		if strings.TrimSpace(instance.InstallationPath) != "" {
			candidates = append(candidates, instance)
		}
	}
	if len(candidates) == 0 {
		return msvcInstance{}, false
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.IsPrerelease != right.IsPrerelease {
			return !left.IsPrerelease
		}
		leftMatch := preferredMajor > 0 && versionMajor(left.InstallationVersion) == preferredMajor
		rightMatch := preferredMajor > 0 && versionMajor(right.InstallationVersion) == preferredMajor
		if leftMatch != rightMatch {
			return leftMatch
		}

		return compareDottedVersions(left.InstallationVersion, right.InstallationVersion) > 0
	})

	return candidates[0], true
}

// versionMajor returns the leading numeric component of a dotted version.
func versionMajor(version string) int {
	major, _, _ := strings.Cut(strings.TrimSpace(version), ".")
	value, err := strconv.Atoi(major)
	if err != nil {
		return 0
	}

	return value
}

// compareDottedVersions numerically compares dotted versions, treating
// missing or non-numeric components as zero.
func compareDottedVersions(left, right string) int {
	leftParts := strings.Split(strings.TrimSpace(left), ".")
	rightParts := strings.Split(strings.TrimSpace(right), ".")
	for index := 0; index < len(leftParts) || index < len(rightParts); index++ {
		leftValue, rightValue := 0, 0
		if index < len(leftParts) {
			leftValue, _ = strconv.Atoi(leftParts[index])
		}
		if index < len(rightParts) {
			rightValue, _ = strconv.Atoi(rightParts[index])
		}
		if leftValue != rightValue {
			if leftValue > rightValue {
				return 1
			}
			return -1
		}
	}

	return 0
}

// detectPHPCompilerMajor maps the PHP runtime's reported compiler to the
// Visual Studio major version (vs17 -> 17), or 0 when it is unknown.
func detectPHPCompilerMajor(phpPath string) int {
	command, err := prepareInternalPHPCommand(phpPath, []string{"-n", "-i"})
	if err != nil {
		return 0
	}
	output, err := command.Output()
	if err != nil {
		return 0
	}
	compiler := parsePECLWindowsCompiler(string(output))
	if len(compiler) < 3 {
		return 0
	}
	major, err := strconv.Atoi(compiler[2:])
	if err != nil {
		return 0
	}

	return major
}

// detectPHPToolset reads the PE header of the PHP executable (or php8.dll) to determine
// the exact MSVC linker version (e.g. "14.44") required by the PHP runtime.
func detectPHPToolset(phpPath string) string {
	target := strings.TrimSpace(phpPath)
	if target == "" {
		return ""
	}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		for _, candidate := range []string{"php.exe", "php8.dll"} {
			full := filepath.Join(target, candidate)
			if fInfo, fErr := os.Stat(full); fErr == nil && !fInfo.IsDir() {
				target = full
				break
			}
		}
	}

	file, err := pe.Open(target)
	if err != nil {
		return ""
	}
	defer file.Close()

	var major, minor uint8
	switch opt := file.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		major = opt.MajorLinkerVersion
		minor = opt.MinorLinkerVersion
	case *pe.OptionalHeader32:
		major = opt.MajorLinkerVersion
		minor = opt.MinorLinkerVersion
	default:
		return ""
	}
	if major == 0 && minor == 0 {
		return ""
	}
	return fmt.Sprintf("%d.%d", major, minor)
}

// selectMSVCToolset checks if the target toolset (e.g. "14.44") is available
// in the Visual Studio instance's VC\Tools\MSVC directory, returning the toolset
// version argument for vcvarsall (e.g. "14.44"), or "" if no matching toolset exists.
func selectMSVCToolset(installationPath, targetToolset string) string {
	targetToolset = strings.TrimSpace(targetToolset)
	if targetToolset == "" || installationPath == "" {
		return ""
	}
	toolsDir := filepath.Join(installationPath, "VC", "Tools", "MSVC")
	entries, err := os.ReadDir(toolsDir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() && (entry.Name() == targetToolset || strings.HasPrefix(entry.Name(), targetToolset+".")) {
			return targetToolset
		}
	}
	return ""
}

// captureMSVCVCVars runs vcvarsall.bat in a fresh cmd.exe and returns the
// environment before and after it ran. Both dumps come from the same cmd.exe
// process so variables cmd.exe itself injects cancel out in the diff. Paths
// travel through environment variables so the ASCII batch file never has to
// encode a non-ASCII Visual Studio location.
func captureMSVCVCVars(vcvarsall, arch, args string) ([]string, []string, error) {
	tempDir, err := os.MkdirTemp("", "polka-msvc-")
	if err != nil {
		return nil, nil, fmt.Errorf("create MSVC capture dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	script := strings.Join([]string{
		"@echo off",
		"set",
		"echo " + msvcCaptureMarker,
		`call "%POLKA_VCVARSALL%" %POLKA_VCVARS_ARCH% %POLKA_VCVARS_ARGS% 1>&2`,
		"if errorlevel 1 exit /b 1",
		"set",
		"",
	}, "\r\n")
	scriptPath := filepath.Join(tempDir, "capture.bat")
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
		return nil, nil, fmt.Errorf("write MSVC capture script: %w", err)
	}

	// /u makes cmd.exe's built-in `set` and `echo` write UTF-16LE to the pipe
	// so non-ASCII variable values survive the round trip.
	command := exec.Command("cmd.exe", "/d", "/u", "/c", scriptPath)
	command.Env = append(os.Environ(),
		"POLKA_VCVARSALL="+vcvarsall,
		"POLKA_VCVARS_ARCH="+arch,
		"POLKA_VCVARS_ARGS="+args,
		"VSCMD_SKIP_SENDTELEMETRY=1",
	)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		// vcvarsall's own echo output is UTF-16 under /u; dropping NULs keeps
		// its ASCII diagnostics readable in the error.
		return nil, nil, fmt.Errorf("run %s %s: %w: %s", vcvarsall, arch, err, strings.TrimSpace(strings.ReplaceAll(stderr.String(), "\x00", "")))
	}

	return splitMSVCCapture(decodeUTF16LE(output))
}

// splitMSVCCapture separates the baseline and post-vcvarsall `set` dumps.
func splitMSVCCapture(output string) ([]string, []string, error) {
	before, after, ok := strings.Cut(output, msvcCaptureMarker)
	if !ok {
		return nil, nil, fmt.Errorf("MSVC capture output is missing its marker")
	}

	return splitEnvironmentLines(before), splitEnvironmentLines(after), nil
}

// splitEnvironmentLines turns `set` output into KEY=VALUE entries.
func splitEnvironmentLines(output string) []string {
	entries := []string{}
	for _, line := range strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, "\r")
		if key, _, ok := strings.Cut(line, "="); ok && strings.TrimSpace(key) != "" {
			entries = append(entries, line)
		}
	}

	return entries
}

// decodeUTF16LE decodes cmd.exe /u output, falling back to the raw bytes when
// the data is not UTF-16.
func decodeUTF16LE(data []byte) string {
	if len(data)%2 != 0 || len(data) == 0 {
		return string(data)
	}
	runes := make([]rune, 0, len(data)/2)
	for index := 0; index+1 < len(data); index += 2 {
		unit := uint16(data[index]) | uint16(data[index+1])<<8
		if unit >= 0xD800 && unit <= 0xDBFF && index+3 < len(data) {
			low := uint16(data[index+2]) | uint16(data[index+3])<<8
			if low >= 0xDC00 && low <= 0xDFFF {
				runes = append(runes, rune((uint32(unit)-0xD800)<<10|(uint32(low)-0xDC00)+0x10000))
				index += 2
				continue
			}
		}
		runes = append(runes, rune(unit))
	}

	return string(runes)
}

// diffMSVCEnvironment returns the variables vcvarsall defined or changed.
// List variables keep only the entries vcvarsall added, so later shells
// prepend them to whatever the user's current value is.
func diffMSVCEnvironment(baseline, captured []string) (map[string]string, map[string][]string) {
	baseValues := map[string]string{}
	for _, entry := range baseline {
		key, value, _ := strings.Cut(entry, "=")
		baseValues[strings.ToUpper(key)] = value
	}

	set := map[string]string{}
	prepend := map[string][]string{}
	for _, entry := range captured {
		key, value, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(key)
		if _, ignored := msvcIgnoredVariables[upper]; ignored || strings.HasPrefix(key, "__") {
			continue
		}
		baseValue, existed := baseValues[upper]
		if existed && baseValue == value {
			continue
		}
		if _, isList := msvcListVariables[upper]; isList {
			if added := addedListEntries(splitSemicolonList(baseValue), splitSemicolonList(value)); len(added) > 0 {
				prepend[upper] = added
			}
			continue
		}
		set[key] = value
	}

	return set, prepend
}

// splitSemicolonList splits a Windows list variable, dropping empty entries.
func splitSemicolonList(value string) []string {
	entries := []string{}
	for _, entry := range strings.Split(value, ";") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			entries = append(entries, trimmed)
		}
	}

	return entries
}

// addedListEntries returns entries of captured missing from baseline, in
// order and without case-insensitive duplicates.
func addedListEntries(baseline, captured []string) []string {
	seen := map[string]struct{}{}
	for _, entry := range baseline {
		seen[strings.ToLower(filepath.Clean(entry))] = struct{}{}
	}
	added := []string{}
	for _, entry := range captured {
		key := strings.ToLower(filepath.Clean(entry))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		added = append(added, entry)
	}

	return added
}

// readMSVCToolchain loads a captured toolchain, returning nil when absent.
func readMSVCToolchain(path string) (*MSVCToolchain, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read MSVC environment %s: %w", path, err)
	}
	var toolchain MSVCToolchain
	if err := json.Unmarshal(data, &toolchain); err != nil {
		return nil, fmt.Errorf("parse MSVC environment %s: %w", path, err)
	}

	return &toolchain, nil
}

// writeMSVCToolchain persists a captured toolchain.
func writeMSVCToolchain(path string, toolchain MSVCToolchain) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create MSVC environment dir: %w", err)
	}
	data, err := json.MarshalIndent(toolchain, "", "  ")
	if err != nil {
		return fmt.Errorf("encode MSVC environment: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write MSVC environment %s: %w", path, err)
	}

	return nil
}
