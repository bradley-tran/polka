package backend

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"
)

// TestSelectMSVCInstancePrefersStableMatchingMajor checks that stable
// releases win over previews and the PHP compiler's major version wins over
// a newer Visual Studio.
func TestSelectMSVCInstancePrefersStableMatchingMajor(t *testing.T) {
	instances := []msvcInstance{
		{InstallationPath: `C:\VS\18-preview`, InstallationVersion: "18.11.1", IsPrerelease: true},
		{InstallationPath: `C:\VS\18`, InstallationVersion: "18.10.12217.157"},
		{InstallationPath: `C:\VS\17`, InstallationVersion: "17.14.36301.6"},
		{InstallationPath: `C:\VS\16`, InstallationVersion: "16.11.5"},
	}

	cases := []struct {
		name           string
		preferredMajor int
		want           string
	}{
		{name: "matching major", preferredMajor: 17, want: `C:\VS\17`},
		{name: "unknown compiler picks newest stable", preferredMajor: 0, want: `C:\VS\18`},
		{name: "missing major falls back to newest stable", preferredMajor: 15, want: `C:\VS\18`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, ok := selectMSVCInstance(instances, testCase.preferredMajor)
			if !ok || got.InstallationPath != testCase.want {
				t.Fatalf("selectMSVCInstance(%d) = %q, %v; want %q", testCase.preferredMajor, got.InstallationPath, ok, testCase.want)
			}
		})
	}
}

// TestSelectMSVCInstanceFallsBackToPrerelease checks a preview install is
// used when it is the only toolchain, and that an empty list reports none.
func TestSelectMSVCInstanceFallsBackToPrerelease(t *testing.T) {
	got, ok := selectMSVCInstance([]msvcInstance{{InstallationPath: `C:\VS\Preview`, InstallationVersion: "18.0", IsPrerelease: true}}, 17)
	if !ok || got.InstallationPath != `C:\VS\Preview` {
		t.Fatalf("selectMSVCInstance(preview only) = %q, %v", got.InstallationPath, ok)
	}
	if _, ok := selectMSVCInstance(nil, 17); ok {
		t.Fatal("selectMSVCInstance(nil) reported a match")
	}
}

// TestParseMSVCInstancesReadsVSWhereJSON checks the vswhere fields Polka
// relies on are decoded.
func TestParseMSVCInstancesReadsVSWhereJSON(t *testing.T) {
	data := []byte(`[{"installationPath":"C:\\VS\\BuildTools","installationVersion":"17.14.1","displayName":"Visual Studio Build Tools 2022","isPrerelease":false,"catalog":{"productDisplayVersion":"17.14.2"}}]`)
	instances, err := parseMSVCInstances(data)
	if err != nil {
		t.Fatalf("parseMSVCInstances() error = %v", err)
	}
	if len(instances) != 1 || instances[0].InstallationPath != `C:\VS\BuildTools` || instances[0].Catalog.ProductDisplayVersion != "17.14.2" {
		t.Fatalf("parseMSVCInstances() = %#v", instances)
	}
}

// TestDiffMSVCEnvironmentSplitsSetAndPrepend checks list variables keep only
// vcvarsall's added entries while other changes are stored verbatim and
// cmd.exe or vcvarsall bookkeeping is dropped.
func TestDiffMSVCEnvironmentSplitsSetAndPrepend(t *testing.T) {
	baseline := []string{
		`Path=C:\Windows;C:\Tools`,
		`PROMPT=$P$G`,
		`UNCHANGED=1`,
	}
	captured := []string{
		`Path=C:\VS\bin;c:\windows;C:\Windows;C:\Tools`,
		`INCLUDE=C:\VS\include;C:\SDK\ucrt;`,
		`PROMPT=$P$G`,
		`UNCHANGED=1`,
		`VCToolsInstallDir=C:\VS\Tools\MSVC\14.44\`,
		`__VSCMD_PREINIT_PATH=C:\Windows;C:\Tools`,
		`POLKA_VCVARSALL=C:\VS\vcvarsall.bat`,
	}

	set, prepend := diffMSVCEnvironment(baseline, captured)
	wantSet := map[string]string{"VCToolsInstallDir": `C:\VS\Tools\MSVC\14.44\`}
	if !reflect.DeepEqual(set, wantSet) {
		t.Fatalf("set = %#v, want %#v", set, wantSet)
	}
	wantPrepend := map[string][]string{
		"PATH":    {`C:\VS\bin`},
		"INCLUDE": {`C:\VS\include`, `C:\SDK\ucrt`},
	}
	if !reflect.DeepEqual(prepend, wantPrepend) {
		t.Fatalf("prepend = %#v, want %#v", prepend, wantPrepend)
	}
}

// TestSplitMSVCCaptureDecodesUTF16 checks cmd.exe /u output is decoded,
// including non-ASCII values, and split at the capture marker.
func TestSplitMSVCCaptureDecodesUTF16(t *testing.T) {
	text := "A=1\r\nNAME=Bảo\r\n" + msvcCaptureMarker + " \r\nA=1\r\nVCToolsInstallDir=C:\\VS\r\n"
	encoded := []byte{}
	for _, unit := range utf16.Encode([]rune(text)) {
		encoded = append(encoded, byte(unit), byte(unit>>8))
	}

	before, after, err := splitMSVCCapture(decodeUTF16LE(encoded))
	if err != nil {
		t.Fatalf("splitMSVCCapture() error = %v", err)
	}
	if want := []string{"A=1", "NAME=Bảo"}; !reflect.DeepEqual(before, want) {
		t.Fatalf("before = %#v, want %#v", before, want)
	}
	if want := []string{"A=1", `VCToolsInstallDir=C:\VS`}; !reflect.DeepEqual(after, want) {
		t.Fatalf("after = %#v, want %#v", after, want)
	}
	if _, _, err := splitMSVCCapture("A=1"); err == nil {
		t.Fatal("splitMSVCCapture(no marker) error = nil")
	}
}

// stubMSVCHost replaces host Visual Studio discovery with a fake instance
// rooted in a temp directory and returns a counter of vcvarsall captures.
func stubMSVCHost(t *testing.T, instances []msvcInstance, listErr error) *int {
	t.Helper()
	vswhere := filepath.Join(t.TempDir(), "vswhere.exe")
	if err := os.WriteFile(vswhere, nil, 0o644); err != nil {
		t.Fatalf("write vswhere stub: %v", err)
	}
	for _, instance := range instances {
		vcvarsall := filepath.Join(instance.InstallationPath, "VC", "Auxiliary", "Build", "vcvarsall.bat")
		if err := os.MkdirAll(filepath.Dir(vcvarsall), 0o755); err != nil {
			t.Fatalf("create vcvarsall dir: %v", err)
		}
		if err := os.WriteFile(vcvarsall, nil, 0o644); err != nil {
			t.Fatalf("write vcvarsall stub: %v", err)
		}
	}

	captures := 0
	originalLocations, originalList, originalCapture, originalMajor := msvcVSWhereLocation, msvcListInstances, msvcCaptureVCVars, msvcDetectPHPMajor
	msvcVSWhereLocation = func() []string { return []string{vswhere} }
	msvcListInstances = func(string) ([]msvcInstance, error) { return instances, listErr }
	msvcCaptureVCVars = func(vcvarsall, arch string) ([]string, []string, error) {
		captures++
		return []string{`Path=C:\Windows`}, []string{
			`Path=C:\VS\bin\Hostx64\x64;C:\Windows`,
			`INCLUDE=C:\VS\include`,
			`VCToolsInstallDir=C:\VS\Tools\`,
		}, nil
	}
	msvcDetectPHPMajor = func(string) int { return 0 }
	t.Cleanup(func() {
		msvcVSWhereLocation, msvcListInstances, msvcCaptureVCVars, msvcDetectPHPMajor = originalLocations, originalList, originalCapture, originalMajor
	})

	return &captures
}

// TestSyncMSVCToolchainCapturesAndReuses checks install records the vcvarsall
// environment, reuses it while the Visual Studio instance is unchanged, and
// recaptures it on force.
func TestSyncMSVCToolchainCapturesAndReuses(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSVC detection only runs on Windows")
	}
	store := NewProjectStore(t.TempDir())
	instance := msvcInstance{
		InstallationPath:    filepath.Join(t.TempDir(), "BuildTools"),
		InstallationVersion: "17.14.1",
		DisplayName:         "Visual Studio Build Tools 2022",
	}
	instance.Catalog.ProductDisplayVersion = "17.14.2"
	captures := stubMSVCHost(t, []msvcInstance{instance}, nil)
	environment := Environment{Name: "default", PHPBuildTools: true}

	for _, force := range []bool{false, false, true} {
		if err := store.syncMSVCToolchain(environment, force, func(message string) { t.Fatalf("unexpected warning: %s", message) }); err != nil {
			t.Fatalf("syncMSVCToolchain(force=%v) error = %v", force, err)
		}
	}
	if *captures != 2 {
		t.Fatalf("vcvarsall captures = %d, want 2 (initial + force)", *captures)
	}

	toolchain, err := store.MSVCToolchain(environment)
	if err != nil || toolchain == nil {
		t.Fatalf("MSVCToolchain() = %#v, %v", toolchain, err)
	}
	if toolchain.Version != "17.14.2" || toolchain.Arch != msvcTargetArch {
		t.Fatalf("toolchain = %#v", toolchain)
	}
	if got := toolchain.Prepend["PATH"]; !reflect.DeepEqual(got, []string{`C:\VS\bin\Hostx64\x64`}) {
		t.Fatalf("toolchain PATH = %#v", got)
	}
	if disabled, err := store.MSVCToolchain(Environment{Name: "default"}); err != nil || disabled != nil {
		t.Fatalf("MSVCToolchain(sdk disabled) = %#v, %v; want nil", disabled, err)
	}
}

// TestSyncMSVCToolchainWarnsWhenMissing checks a host without MSVC does not
// fail install, reports why, and drops any stale capture.
func TestSyncMSVCToolchainWarnsWhenMissing(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSVC detection only runs on Windows")
	}
	store := NewProjectStore(t.TempDir())
	environment := Environment{Name: "default", PHPBuildTools: true}
	if err := writeMSVCToolchain(store.msvcStateFile("default"), MSVCToolchain{Version: "stale"}); err != nil {
		t.Fatalf("write stale toolchain: %v", err)
	}

	for _, testCase := range []struct {
		name    string
		listErr error
		want    string
	}{
		{name: "no instances", want: "no Visual Studio installation"},
		{name: "vswhere failure", listErr: errors.New("vswhere exploded"), want: "vswhere exploded"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			stubMSVCHost(t, nil, testCase.listErr)
			warnings := []string{}
			if err := store.syncMSVCToolchain(environment, false, func(message string) { warnings = append(warnings, message) }); err != nil {
				t.Fatalf("syncMSVCToolchain() error = %v", err)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], testCase.want) {
				t.Fatalf("warnings = %#v, want one containing %q", warnings, testCase.want)
			}
			if toolchain, err := store.MSVCToolchain(environment); err != nil || toolchain != nil {
				t.Fatalf("MSVCToolchain() = %#v, %v; want stale capture removed", toolchain, err)
			}
		})
	}
}
