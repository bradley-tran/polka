package tools

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

func phpDevelPlugin() Plugin {
	return newManifestPlugin(PHPDevel, pluginHooks{
		download: func(ctx DownloadContext) error {
			return downloadPHPDevel(ctx.Client, ctx.CacheDir, ctx.Version)
		},
		postInstall: func(ctx InstallContext) error {
			return postInstallPHPDevel(ctx)
		},
	})
}

// postInstallPHPDevel applies Polka patches to the installed PHP devel pack on Windows.
func postInstallPHPDevel(ctx InstallContext) error {
	targetDir := resolvePHPDevelTargetDir(ctx)
	if targetDir == "" {
		return nil
	}
	return PatchPHPDevel(targetDir)
}

func resolvePHPDevelTargetDir(ctx InstallContext) string {
	target := strings.TrimSpace(ctx.Result.TargetPath)
	if target != "" {
		if info, err := os.Stat(target); err == nil && !info.IsDir() {
			return filepath.Dir(target)
		}
		if filepath.Ext(target) != "" {
			return filepath.Dir(target)
		}
		return target
	}
	if ctx.EnvsDir != "" && ctx.Result.Version != "" {
		return filepath.Join(ctx.EnvsDir, PHPDevel, ctx.Result.Version)
	}
	return ""
}

const polkaPHPDevelPatchMarker = "// Polka auto-patch: dynamically resolve PHP_PREFIX to the matching Polka PHP runtime"

var (
	rePhpizePHPDir    = regexp.MustCompile(`(?m)^(var\s+PHP_DIR\s*=\s*FSO\.GetParentFolderName\(WScript\.ScriptFullName\)\.replace\([^\)]+\);?\r?)$`)
	reConfigW32Prefix = regexp.MustCompile(`(?s)if\s*\(\s*PHP_PREFIX\s*==\s*''\s*\)\s*\{[\s\S]*?DEFINE\(\s*'PHP_PREFIX'\s*,\s*PHP_PREFIX\s*\);`)
)

// PatchPHPDevel updates the Windows php-devel scripts so that PHP_PREFIX resolves
// dynamically to the matching Polka PHP runtime instead of the upstream hardcoded C:\php.
// If targetDir points to an executable (such as phpize.bat), its containing directory is used.
func PatchPHPDevel(targetDir string) error {
	targetDir = strings.TrimSpace(targetDir)
	if targetDir == "" {
		return nil
	}
	info, err := os.Stat(targetDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		targetDir = filepath.Dir(targetDir)
	}

	if err := patchPHPDevelScript(targetDir, filepath.Join("script", "phpize.js"), patchPhpizeJS); err != nil {
		return fmt.Errorf("patch phpize.js: %w", err)
	}
	if err := patchPHPDevelScript(targetDir, filepath.Join("script", "config.w32.phpize.in"), patchConfigW32PhpizeIn); err != nil {
		return fmt.Errorf("patch config.w32.phpize.in: %w", err)
	}
	return nil
}

func patchPHPDevelScript(targetDir, relativePath string, patchFn func(string) (string, error)) error {
	fullPath := filepath.Join(targetDir, relativePath)
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("php-devel file %q not found under %s", relativePath, targetDir)
		}
		return err
	}
	content := string(contentBytes)
	if strings.Contains(content, polkaPHPDevelPatchMarker) {
		return nil
	}
	patched, err := patchFn(content)
	if err != nil {
		return err
	}
	if patched == content {
		return nil
	}
	return os.WriteFile(fullPath, []byte(patched), 0o644)
}

func detectLineEnding(s string) string {
	if strings.Contains(s, "\r\n") {
		return "\r\n"
	}
	return "\n"
}

func patchPhpizeJS(content string) (string, error) {
	if strings.Contains(content, polkaPHPDevelPatchMarker) {
		return content, nil
	}
	nl := detectLineEnding(content)
	snippet := nl + polkaPHPDevelPatchMarker + nl +
		"(function() {" + nl +
		"\ttry {" + nl +
		"\t\tvar envShell = WScript.CreateObject(\"WScript.Shell\");" + nl +
		"\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");" + nl +
		"\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {" + nl +
		"\t\t\tPHP_PREFIX = envPrefix;" + nl +
		"\t\t\treturn;" + nl +
		"\t\t}" + nl +
		"\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));" + nl +
		"\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";" + nl +
		"\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);" + nl +
		"\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {" + nl +
		"\t\t\tPHP_PREFIX = polkaCandidate;" + nl +
		"\t\t}" + nl +
		"\t} catch (e) {}" + nl +
		"})();"

	loc := rePhpizePHPDir.FindStringIndex(content)
	if loc == nil {
		return content, fmt.Errorf("could not find PHP_DIR anchor line in phpize.js")
	}

	patched := content[:loc[1]] + snippet + content[loc[1]:]
	return patched, nil
}

func patchConfigW32PhpizeIn(content string) (string, error) {
	if strings.Contains(content, polkaPHPDevelPatchMarker) {
		return content, nil
	}
	nl := detectLineEnding(content)
	replacement := "if (PHP_PREFIX == '' || PHP_PREFIX == \"C:\\\\php\") {" + nl +
		"\t" + polkaPHPDevelPatchMarker + nl +
		"\ttry {" + nl +
		"\t\tvar envShell = WshShell;" + nl +
		"\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");" + nl +
		"\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {" + nl +
		"\t\t\tPHP_PREFIX = envPrefix;" + nl +
		"\t\t} else {" + nl +
		"\t\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));" + nl +
		"\t\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";" + nl +
		"\t\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);" + nl +
		"\t\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {" + nl +
		"\t\t\t\tPHP_PREFIX = polkaCandidate;" + nl +
		"\t\t\t}" + nl +
		"\t\t}" + nl +
		"\t} catch (e) {}" + nl +
		"\tif (PHP_PREFIX == '') {" + nl +
		"\t\tPHP_PREFIX = \"C:\\\\php\";" + nl +
		"\t}" + nl +
		"\tif (PHP_DEBUG == \"yes\")" + nl +
		"\t\tPHP_PREFIX += \"\\\\debug\";" + nl +
		"}" + nl +
		"DEFINE('PHP_PREFIX', PHP_PREFIX);"

	loc := reConfigW32Prefix.FindStringIndex(content)
	if loc == nil {
		return content, fmt.Errorf("could not find PHP_PREFIX block in config.w32.phpize.in")
	}

	patched := content[:loc[0]] + replacement + content[loc[1]:]
	return patched, nil
}

// downloadPHPDevel fetches the devel pack from the exact Windows PHP release
// variant selected for the managed runtime.
func downloadPHPDevel(client *http.Client, cacheDir, version string) error {
	if runtime.GOOS != "windows" {
		return fmt.Errorf("automatic php-devel download is only implemented on Windows")
	}
	runtimeVersion, threadSafe := parsePHPDevelCacheVersion(version)
	if err := os.MkdirAll(filepath.Join(cacheDir, PHPDevel), 0o755); err != nil {
		return fmt.Errorf("create php-devel cache dir: %w", err)
	}

	index, err := fetchPHPWindowsReleaseIndex(client)
	if err != nil {
		return err
	}
	release, ok := index[phpSeries(runtimeVersion)]
	if !ok {
		return fmt.Errorf("php version %q is not available in the Windows release index", runtimeVersion)
	}
	variant, err := selectPHPWindowsVariant(release, threadSafe)
	if err != nil {
		return err
	}
	asset := variant.DevelPack
	if strings.TrimSpace(asset.Path) == "" || strings.TrimSpace(asset.SHA256) == "" {
		return fmt.Errorf("Windows PHP %s release does not publish a matching devel pack", release.Version)
	}

	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, PHPDevel), version+"-tmp-")
	if err != nil {
		return fmt.Errorf("create php-devel staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, filepath.Base(asset.Path))
	archiveURL := fmt.Sprintf("%s/%s", phpWindowsBaseURL, asset.Path)
	if err := downloadFile(client, archiveURL, archivePath); err != nil {
		return err
	}
	if err := verifyChecksum(asset.SHA256, archivePath); err != nil {
		return err
	}

	_, err = cacheArchivePayload(cacheDir, PHPDevel, version, release.Version, downloadAsset{
		FileName:          filepath.Base(asset.Path),
		URL:               archiveURL,
		Checksum:          asset.SHA256,
		ChecksumAlgorithm: checksumAlgorithmSHA256,
		ArchiveFormat:     archiveFormatZip,
	}, archivePath)
	return err
}

// parsePHPDevelCacheVersion separates the backend's cache-only flavor suffix
// from the user-visible PHP version.
func parsePHPDevelCacheVersion(version string) (string, bool) {
	trimmed := strings.TrimSpace(version)
	if strings.HasSuffix(trimmed, "-zts") {
		return strings.TrimSuffix(trimmed, "-zts"), true
	}
	if strings.HasSuffix(trimmed, "-nts") {
		return strings.TrimSuffix(trimmed, "-nts"), false
	}

	return trimmed, false
}
