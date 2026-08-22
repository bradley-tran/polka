package backend

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"polka/config"
	"polka/tools"
)

var (
	peclRESTBaseURL    = "https://pecl.php.net/rest"
	peclWindowsBaseURL = "https://downloads.php.net/~windows/pecl/releases"
	peclHrefPattern    = regexp.MustCompile(`(?i)href=["']([^"']+)["']`)
)

const peclInstallStateFileName = "pecl-installed.json"

// PECLExtensionResult describes a resolved and installed legacy PECL package.
type PECLExtensionResult struct {
	Package string
	Version string
	Module  string
	Zend    bool
}

type peclPHPInfo struct {
	Version  string
	Series   string
	ZTS      bool
	Arch     string
	Compiler string
}

type peclReleaseList struct {
	Releases []struct {
		Version string `xml:"v"`
		State   string `xml:"s"`
	} `xml:"r"`
}

type peclReleaseDocument struct {
	Version     string `xml:"v"`
	State       string `xml:"st"`
	DownloadURL string `xml:"g"`
}

type peclPackageDocument struct {
	Name    string `xml:"name"`
	Version struct {
		Release string `xml:"release"`
	} `xml:"version"`
	Dependencies struct {
		Required struct {
			PHP struct {
				Min      string   `xml:"min"`
				Max      string   `xml:"max"`
				Excluded []string `xml:"exclude"`
			} `xml:"php"`
			Extensions []struct {
				Name string `xml:"name"`
			} `xml:"extension"`
			Packages []struct {
				Name string `xml:"name"`
			} `xml:"package"`
		} `xml:"required"`
	} `xml:"dependencies"`
	ProvidesExtension string `xml:"providesextension"`
	ExtSource         *struct {
		Options []peclConfigureOption `xml:"configureoption"`
	} `xml:"extsrcrelease"`
	ZendExtSource *struct {
		Options []peclConfigureOption `xml:"configureoption"`
	} `xml:"zendextsrcrelease"`
	Contents peclPackageDir `xml:"contents>dir"`
}

type peclConfigureOption struct {
	Name    string `xml:"name,attr"`
	Default string `xml:"default,attr"`
	Prompt  string `xml:"prompt,attr"`
}

type peclPackageDir struct {
	Name  string            `xml:"name,attr"`
	Files []peclPackageFile `xml:"file"`
	Dirs  []peclPackageDir  `xml:"dir"`
}

type peclPackageFile struct {
	Name string `xml:"name,attr"`
	MD5  string `xml:"md5sum,attr"`
}

type peclResolution struct {
	Package     string
	Version     string
	Module      string
	Zend        bool
	DownloadURL string
	WindowsURL  string
	Configure   map[string]peclConfigureOption
	Document    peclPackageDocument
}

type peclInstallState struct {
	Packages map[string]peclInstalledPackage `json:"packages"`
}

type peclInstalledPackage struct {
	Version string              `json:"version"`
	Module  string              `json:"module"`
	Zend    bool                `json:"zend,omitempty"`
	Files   []peclInstalledFile `json:"files"`
}

type peclInstalledFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// InstallPECLExtension installs one legacy PECL package against an
// environment's standalone PHP runtime. PIE should be preferred when the
// extension publishes a PIE-compatible vendor/name package.
func (s Store) InstallPECLExtension(stdout, stderr io.Writer, environmentName, packageName, requestedVersion string, configureOptions map[string]string) (PECLExtensionResult, error) {
	environment, _, err := s.installEnvironment(environmentName)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	projectPHP, tool, version, err := s.resolvePECLTargetPHP(environment)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	resolution, err := s.resolvePECLPackage(projectPHP, packageName, requestedVersion)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	if err := validatePECLConfigureOptions(resolution, configureOptions); err != nil {
		return PECLExtensionResult{}, err
	}
	if err := rejectPECLPIEConflict(environment, resolution.Module); err != nil {
		return PECLExtensionResult{}, err
	}
	if runtime.GOOS == "windows" && len(configureOptions) > 0 {
		_, _ = fmt.Fprintln(stderr, "warning: PECL configure options are persisted for Linux builds but do not affect the prebuilt Windows DLL")
	}
	if err := s.installResolvedPECLExtension(stdout, stderr, projectPHP, tool, version, resolution, configureOptions); err != nil {
		return PECLExtensionResult{}, err
	}

	return PECLExtensionResult{Package: resolution.Package, Version: resolution.Version, Module: resolution.Module, Zend: resolution.Zend}, nil
}

// RemovePECLExtension removes files recorded for one Polka-managed PECL
// package. Unknown or modified files are retained with a warning.
func (s Store) RemovePECLExtension(stderr io.Writer, environmentName, packageName string) (PECLExtensionResult, error) {
	environment, _, err := s.installEnvironment(environmentName)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	_, tool, version, err := s.resolvePECLTargetPHP(environment)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	installRoot := filepath.Join(s.EnvsDir, tool, version)
	state, err := readPECLInstallState(installRoot)
	if err != nil {
		return PECLExtensionResult{}, err
	}
	packageName = strings.ToLower(strings.TrimSpace(packageName))
	record, ok := state.Packages[packageName]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "warning: no tracked PECL files found for %s; removing its config entry only\n", packageName)
		return PECLExtensionResult{Package: packageName, Module: packageName}, nil
	}

	for _, file := range record.Files {
		if peclFileReferencedByOtherPackage(state, packageName, file.Path) {
			continue
		}
		path, err := peclStateArtifactPath(installRoot, file.Path)
		if err != nil {
			return PECLExtensionResult{}, err
		}
		actual, err := fileSHA256(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return PECLExtensionResult{}, err
		}
		if !strings.EqualFold(actual, file.SHA256) {
			_, _ = fmt.Fprintf(stderr, "warning: leaving modified PECL artifact %s\n", path)
			continue
		}
		if err := os.Remove(path); err != nil {
			return PECLExtensionResult{}, fmt.Errorf("remove PECL artifact %s: %w; stop the environment and retry", path, err)
		}
	}
	delete(state.Packages, packageName)
	if err := writePECLInstallState(installRoot, state); err != nil {
		return PECLExtensionResult{}, err
	}

	return PECLExtensionResult{Package: packageName, Version: record.Version, Module: record.Module, Zend: record.Zend}, nil
}

func (s Store) resolvePECLTargetPHP(environment Environment) (string, string, string, error) {
	tool, version := config.PrimaryPHPTool(environment)
	if tool == "" {
		return "", "", "", fmt.Errorf("environment %q does not define a standalone php or php-zts runtime; legacy PECL cannot target FrankenPHP's embedded PHP", environment.Name)
	}
	path, err := s.resolveInstalledTool(tool, version)
	if err != nil {
		return "", "", "", fmt.Errorf("%s %s is not installed for environment %q; run `polka install` first", tool, version, environment.Name)
	}

	return path, tool, version, nil
}

func (s Store) resolvePECLPackage(phpPath, packageName, requestedVersion string) (peclResolution, error) {
	packageName = strings.ToLower(strings.TrimSpace(packageName))
	if err := config.ValidatePECLExtensionPackage(packageName); err != nil {
		return peclResolution{}, err
	}
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		requestedVersion = "*"
	}
	if err := config.ValidatePECLExtensionVersion(packageName, requestedVersion); err != nil {
		return peclResolution{}, err
	}
	phpInfo, err := inspectPECLPHP(phpPath)
	if err != nil {
		return peclResolution{}, err
	}
	client := s.peclHTTPClient()
	versions := []string{requestedVersion}
	if requestedVersion == "*" {
		var releases peclReleaseList
		if err := peclDownloadXML(client, peclRESTURL("r", packageName, "allreleases.xml"), &releases); err != nil {
			return peclResolution{}, fmt.Errorf("resolve legacy PECL package %s: %w", packageName, err)
		}
		versions = versions[:0]
		for _, release := range releases.Releases {
			if strings.EqualFold(strings.TrimSpace(release.State), "stable") {
				versions = append(versions, strings.TrimSpace(release.Version))
			}
		}
	}
	var lastErr error
	for _, candidate := range versions {
		resolution, err := resolvePECLRelease(client, packageName, candidate, phpInfo)
		if err == nil {
			if err := validatePECLPackageDependencies(phpPath, resolution); err != nil {
				lastErr = err
				if requestedVersion != "*" {
					return peclResolution{}, err
				}
				continue
			}
			return resolution, nil
		}
		if requestedVersion != "*" {
			return peclResolution{}, err
		}
		lastErr = err
	}
	if lastErr != nil {
		return peclResolution{}, fmt.Errorf("no usable stable %s PECL release for PHP %s: %w", packageName, phpInfo.Version, lastErr)
	}

	return peclResolution{}, fmt.Errorf("no stable %s PECL release is compatible with PHP %s on %s/%s", packageName, phpInfo.Version, runtime.GOOS, runtime.GOARCH)
}

func validatePECLPackageDependencies(phpPath string, resolution peclResolution) error {
	modules, err := tools.InstalledPHPModules(phpPath)
	if err != nil {
		return err
	}
	for _, dependency := range resolution.Document.Dependencies.Required.Extensions {
		name := strings.ToLower(strings.TrimSpace(dependency.Name))
		if name != "" && !modules[name] {
			return fmt.Errorf("PECL %s %s requires PHP extension %s; enable or install it first", resolution.Package, resolution.Version, name)
		}
	}
	for _, dependency := range resolution.Document.Dependencies.Required.Packages {
		name := strings.ToLower(strings.TrimSpace(dependency.Name))
		if name != "" && name != resolution.Package && !modules[name] {
			return fmt.Errorf("PECL %s %s requires PECL package %s; install it explicitly first", resolution.Package, resolution.Version, name)
		}
	}

	return nil
}

func resolvePECLRelease(client *http.Client, packageName, version string, phpInfo peclPHPInfo) (peclResolution, error) {
	var release peclReleaseDocument
	if err := peclDownloadXML(client, peclRESTURL("r", packageName, version+".xml"), &release); err != nil {
		return peclResolution{}, err
	}
	var document peclPackageDocument
	if err := peclDownloadXML(client, peclRESTURL("r", packageName, "package."+version+".xml"), &document); err != nil {
		return peclResolution{}, err
	}
	if !peclPHPVersionCompatible(phpInfo.Version, document.Dependencies.Required.PHP.Min, document.Dependencies.Required.PHP.Max, document.Dependencies.Required.PHP.Excluded) {
		return peclResolution{}, fmt.Errorf("PECL %s %s does not support PHP %s", packageName, version, phpInfo.Version)
	}
	module := strings.ToLower(strings.TrimSpace(document.ProvidesExtension))
	if module == "" {
		module = packageName
	}
	resolution := peclResolution{
		Package:     packageName,
		Version:     strings.TrimSpace(release.Version),
		Module:      module,
		Zend:        document.ZendExtSource != nil,
		DownloadURL: strings.TrimSpace(release.DownloadURL),
		Configure:   map[string]peclConfigureOption{},
		Document:    document,
	}
	if resolution.Version == "" {
		resolution.Version = version
	}
	var options []peclConfigureOption
	if document.ExtSource != nil {
		options = document.ExtSource.Options
	}
	if document.ZendExtSource != nil {
		options = document.ZendExtSource.Options
	}
	for _, option := range options {
		name := normalizePECLConfigureOptionName(option.Name)
		if name != "" {
			resolution.Configure[name] = option
		}
	}
	if runtime.GOOS == "windows" {
		url, err := resolvePECLWindowsArchive(client, packageName, resolution.Version, module, phpInfo)
		if err != nil {
			return peclResolution{}, err
		}
		resolution.WindowsURL = url
	}

	return resolution, nil
}

func (s Store) installResolvedPECLExtension(stdout, stderr io.Writer, phpPath, tool, phpVersion string, resolution peclResolution, options map[string]string) error {
	installRoot := filepath.Join(s.EnvsDir, tool, phpVersion)
	state, err := readPECLInstallState(installRoot)
	if err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Join(s.CacheDir, "pecl"), resolution.Package+"-")
	if err != nil {
		if err := os.MkdirAll(filepath.Join(s.CacheDir, "pecl"), 0o755); err != nil {
			return fmt.Errorf("create PECL cache directory: %w", err)
		}
		staging, err = os.MkdirTemp(filepath.Join(s.CacheDir, "pecl"), resolution.Package+"-")
	}
	if err != nil {
		return fmt.Errorf("create PECL staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	var artifacts []string
	if runtime.GOOS == "windows" {
		artifacts, err = s.installPECLWindowsPayload(stdout, installRoot, phpPath, resolution, staging, state)
	} else if runtime.GOOS == "linux" {
		artifacts, err = s.installPECLLinuxPayload(stdout, stderr, installRoot, phpPath, resolution, options, staging, state)
	} else {
		err = fmt.Errorf("legacy PECL installation is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	if err != nil {
		return err
	}

	files := make([]peclInstalledFile, 0, len(artifacts))
	for _, artifact := range artifacts {
		digest, err := fileSHA256(artifact)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(installRoot, artifact)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
			return fmt.Errorf("PECL artifact %s is outside PHP install root", artifact)
		}
		files = append(files, peclInstalledFile{Path: filepath.ToSlash(relative), SHA256: digest})
	}
	old := state.Packages[resolution.Package]
	state.Packages[resolution.Package] = peclInstalledPackage{Version: resolution.Version, Module: resolution.Module, Zend: resolution.Zend, Files: files}
	if err := writePECLInstallState(installRoot, state); err != nil {
		return err
	}
	for _, oldFile := range old.Files {
		if peclRecordContainsPath(files, oldFile.Path) || peclFileReferencedByOtherPackage(state, resolution.Package, oldFile.Path) {
			continue
		}
		path, pathErr := peclStateArtifactPath(installRoot, oldFile.Path)
		if pathErr == nil {
			actual, hashErr := fileSHA256(path)
			if hashErr == nil && strings.EqualFold(actual, oldFile.SHA256) {
				_ = os.Remove(path)
			}
		}
	}

	return nil
}

func (s Store) installPECLWindowsPayload(stdout io.Writer, installRoot, phpPath string, resolution peclResolution, staging string, state peclInstallState) ([]string, error) {
	archive, err := s.cachedPECLPayload(resolution.Package, resolution.Version, filepath.Base(resolution.WindowsURL), resolution.WindowsURL)
	if err != nil {
		return nil, err
	}
	extracted := filepath.Join(staging, "payload")
	if err := extractPECLZip(archive, extracted); err != nil {
		return nil, err
	}
	extensionName := "php_" + strings.ToLower(resolution.Module) + ".dll"
	moduleSource, err := findPECLFile(extracted, extensionName)
	if err != nil {
		return nil, fmt.Errorf("PECL Windows archive does not contain %s: %w", extensionName, err)
	}
	extDir := filepath.Join(installRoot, "ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return nil, err
	}
	moduleTarget := filepath.Join(extDir, extensionName)
	tracked, err := installPECLArtifact(moduleSource, moduleTarget, installRoot, state, resolution.Package, false)
	if err != nil {
		return nil, err
	}
	artifacts := []string{}
	if tracked {
		artifacts = append(artifacts, moduleTarget)
	}
	phpDir := filepath.Dir(phpPath)
	err = filepath.WalkDir(extracted, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".dll") || strings.EqualFold(entry.Name(), extensionName) {
			return walkErr
		}
		target := filepath.Join(phpDir, entry.Name())
		tracked, err := installPECLArtifact(path, target, installRoot, state, resolution.Package, true)
		if err != nil {
			return err
		}
		if tracked {
			artifacts = append(artifacts, target)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(stdout, "Installed legacy PECL binary %s %s for PHP\n", resolution.Package, resolution.Version)

	return artifacts, nil
}

func (s Store) installPECLLinuxPayload(stdout, stderr io.Writer, installRoot, phpPath string, resolution peclResolution, overrides map[string]string, staging string, state peclInstallState) ([]string, error) {
	fileName := resolution.Package + "-" + resolution.Version + ".tgz"
	archive, err := s.cachedPECLPayload(resolution.Package, resolution.Version, fileName, resolution.DownloadURL)
	if err != nil {
		return nil, err
	}
	sourceRoot := filepath.Join(staging, "source")
	if err := extractPECLTarGz(archive, sourceRoot); err != nil {
		return nil, err
	}
	buildRoot, err := findPECLBuildRoot(sourceRoot)
	if err != nil {
		return nil, err
	}
	if err := verifyPECLPackageFiles(buildRoot, resolution.Document.Contents); err != nil {
		return nil, err
	}
	phpize, phpConfig, err := resolvePECLBuildTools(installRoot, phpPath)
	if err != nil {
		return nil, err
	}
	if err := runPECLBuildCommand(stdout, stderr, buildRoot, phpize); err != nil {
		return nil, err
	}
	configure := filepath.Join(buildRoot, "configure")
	args := []string{"--with-php-config=" + phpConfig}
	optionNames := make([]string, 0, len(resolution.Configure))
	for name := range resolution.Configure {
		optionNames = append(optionNames, name)
	}
	sort.Strings(optionNames)
	for _, name := range optionNames {
		value := strings.TrimSpace(resolution.Configure[name].Default)
		if override, ok := overrides[name]; ok {
			value = strings.TrimSpace(override)
		}
		args = append(args, "--"+name+"="+value)
	}
	if err := runPECLBuildCommand(stdout, stderr, buildRoot, configure, args...); err != nil {
		return nil, err
	}
	if err := runPECLBuildCommand(stdout, stderr, buildRoot, "make"); err != nil {
		return nil, err
	}
	moduleSource, err := findPECLFile(filepath.Join(buildRoot, "modules"), resolution.Module+".so")
	if err != nil {
		return nil, fmt.Errorf("locate built PECL module %s.so: %w", resolution.Module, err)
	}
	extDir := filepath.Join(installRoot, "ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		return nil, err
	}
	target := filepath.Join(extDir, resolution.Module+".so")
	tracked, err := installPECLArtifact(moduleSource, target, installRoot, state, resolution.Package, false)
	if err != nil {
		return nil, err
	}
	if !tracked {
		return nil, fmt.Errorf("built PECL module %s was not installed", resolution.Module)
	}

	return []string{target}, nil
}

func (s Store) cachedPECLPayload(packageName, version, fileName, url string) (string, error) {
	if strings.TrimSpace(url) == "" {
		return "", fmt.Errorf("PECL %s %s does not publish a download URL for %s", packageName, version, runtime.GOOS)
	}
	directory := filepath.Join(s.CacheDir, "pecl", packageName, version)
	path := filepath.Join(directory, filepath.Base(fileName))
	checksumPath := path + ".sha256"
	if expected, err := os.ReadFile(checksumPath); err == nil {
		actual, hashErr := fileSHA256(path)
		if hashErr == nil && strings.EqualFold(strings.TrimSpace(string(expected)), actual) {
			return path, nil
		}
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(directory, "download-")
	if err != nil {
		return "", err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	response, err := s.peclHTTPClient().Get(url)
	if err != nil {
		temporary.Close()
		return "", fmt.Errorf("download PECL payload %s: %w", url, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		temporary.Close()
		return "", fmt.Errorf("download PECL payload %s: unexpected status %s", url, response.Status)
	}
	if _, err := io.Copy(temporary, response.Body); err != nil {
		temporary.Close()
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	digest, err := fileSHA256(temporaryPath)
	if err != nil {
		return "", err
	}
	_ = os.Remove(path)
	if err := os.Rename(temporaryPath, path); err != nil {
		return "", err
	}
	if err := os.WriteFile(checksumPath, []byte(digest+"\n"), 0o644); err != nil {
		return "", err
	}

	return path, nil
}

func (s Store) peclHTTPClient() *http.Client {
	switch downloader := s.Downloader.(type) {
	case HTTPToolDownloader:
		if downloader.Client != nil {
			return downloader.Client
		}
	case *HTTPToolDownloader:
		if downloader != nil && downloader.Client != nil {
			return downloader.Client
		}
	}

	return &http.Client{Timeout: 10 * time.Minute}
}

func inspectPECLPHP(phpPath string) (peclPHPInfo, error) {
	script := `echo PHP_VERSION,"~",PHP_MAJOR_VERSION,".",PHP_MINOR_VERSION,"~",PHP_ZTS ? "1" : "0","~",PHP_INT_SIZE === 8 ? "x64" : "x86";`
	command, err := prepareInternalPHPCommand(phpPath, []string{"-n", "-r", script})
	if err != nil {
		return peclPHPInfo{}, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		return peclPHPInfo{}, fmt.Errorf("inspect target PHP: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	parts := strings.Split(strings.TrimSpace(string(output)), "~")
	if len(parts) != 4 {
		return peclPHPInfo{}, fmt.Errorf("inspect target PHP: unexpected output %q", strings.TrimSpace(string(output)))
	}

	info := peclPHPInfo{Version: parts[0], Series: parts[1], ZTS: parts[2] == "1", Arch: parts[3]}
	if runtime.GOOS == "windows" {
		command, err := prepareInternalPHPCommand(phpPath, []string{"-n", "-i"})
		if err == nil {
			if output, outputErr := command.Output(); outputErr == nil {
				info.Compiler = parsePECLWindowsCompiler(string(output))
			}
		}
	}

	return info, nil
}

func parsePECLWindowsCompiler(output string) string {
	lower := strings.ToLower(output)
	switch {
	case strings.Contains(lower, "visual c++ 2022"), strings.Contains(lower, "msvc17"):
		return "vs17"
	case strings.Contains(lower, "visual c++ 2019"), strings.Contains(lower, "msvc16"):
		return "vs16"
	case strings.Contains(lower, "visual c++ 2017"), strings.Contains(lower, "msvc15"):
		return "vc15"
	default:
		return ""
	}
}

func resolvePECLWindowsArchive(client *http.Client, packageName, version, module string, phpInfo peclPHPInfo) (string, error) {
	base := strings.TrimRight(peclWindowsBaseURL, "/") + "/" + packageName + "/" + version + "/"
	response, err := client.Get(base)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("PECL %s %s has no Windows DLL archive", packageName, version)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", err
	}
	mode := "nts"
	if phpInfo.ZTS {
		mode = "ts"
	}
	needle := "-" + strings.ToLower(phpInfo.Series) + "-" + mode + "-"
	candidates := []string{}
	for _, match := range peclHrefPattern.FindAllStringSubmatch(string(data), -1) {
		href := html.UnescapeString(strings.TrimSpace(match[1]))
		lower := strings.ToLower(filepath.Base(href))
		compilerMatches := phpInfo.Compiler == "" || strings.Contains(lower, "-"+strings.ToLower(phpInfo.Compiler)+"-")
		if compilerMatches && strings.HasSuffix(lower, "-"+strings.ToLower(phpInfo.Arch)+".zip") && strings.Contains(lower, needle) && strings.HasPrefix(lower, "php_") {
			candidates = append(candidates, href)
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("PECL %s %s has no Windows %s %s %s DLL", packageName, version, phpInfo.Series, strings.ToUpper(mode), phpInfo.Arch)
	}
	sort.Strings(candidates)

	return base + strings.TrimLeft(candidates[len(candidates)-1], "/"), nil
}

func peclDownloadXML(client *http.Client, url string, target any) error {
	response, err := client.Get(url)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}
	if err := xml.NewDecoder(response.Body).Decode(target); err != nil {
		return fmt.Errorf("decode %s: %w", url, err)
	}

	return nil
}

func peclRESTURL(parts ...string) string {
	return strings.TrimRight(peclRESTBaseURL, "/") + "/" + strings.Join(parts, "/")
}

func peclPHPVersionCompatible(version, min, max string, excluded []string) bool {
	if strings.TrimSpace(min) != "" && comparePECLVersions(version, min) < 0 {
		return false
	}
	if strings.TrimSpace(max) != "" && comparePECLVersions(version, max) > 0 {
		return false
	}
	for _, excludedVersion := range excluded {
		if comparePECLVersions(version, excludedVersion) == 0 {
			return false
		}
	}

	return true
}

func comparePECLVersions(left, right string) int {
	parse := func(value string) []int {
		matches := regexp.MustCompile(`[0-9]+`).FindAllString(value, -1)
		parts := make([]int, len(matches))
		for index, match := range matches {
			parts[index], _ = strconv.Atoi(match)
		}
		return parts
	}
	leftParts, rightParts := parse(left), parse(right)
	count := len(leftParts)
	if len(rightParts) > count {
		count = len(rightParts)
	}
	for index := 0; index < count; index++ {
		var leftPart, rightPart int
		if index < len(leftParts) {
			leftPart = leftParts[index]
		}
		if index < len(rightParts) {
			rightPart = rightParts[index]
		}
		if leftPart < rightPart {
			return -1
		}
		if leftPart > rightPart {
			return 1
		}
	}

	return 0
}

func validatePECLConfigureOptions(resolution peclResolution, options map[string]string) error {
	for name := range options {
		normalized := normalizePECLConfigureOptionName(name)
		if _, ok := resolution.Configure[normalized]; !ok {
			return fmt.Errorf("PECL %s %s does not declare configure option %q", resolution.Package, resolution.Version, name)
		}
	}

	return nil
}

func normalizePECLConfigureOptionName(name string) string {
	return strings.ToLower(strings.TrimLeft(strings.TrimSpace(name), "-"))
}

func rejectPECLPIEConflict(environment Environment, module string) error {
	module = strings.ToLower(strings.TrimSpace(module))
	for pkg := range environment.PIEExtensions {
		if tools.PIEExtensionModuleName(pkg) == module {
			return fmt.Errorf("PHP module %s is configured through both PIE package %s and legacy PECL; remove one provider", module, pkg)
		}
	}

	return nil
}

func readPECLInstallState(installRoot string) (peclInstallState, error) {
	state := peclInstallState{Packages: map[string]peclInstalledPackage{}}
	data, err := os.ReadFile(filepath.Join(installRoot, peclInstallStateFileName))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, fmt.Errorf("decode PECL install state: %w", err)
	}
	if state.Packages == nil {
		state.Packages = map[string]peclInstalledPackage{}
	}

	return state, nil
}

func writePECLInstallState(installRoot string, state peclInstallState) error {
	path := filepath.Join(installRoot, peclInstallStateFileName)
	if len(state.Packages) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(installRoot, "pecl-state-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(path)
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("write PECL install state: %w", err)
	}

	return nil
}

func peclStateArtifactPath(installRoot, relative string) (string, error) {
	root := filepath.Clean(installRoot)
	path := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	if path != root && !strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("PECL state path %q escapes PHP install root", relative)
	}

	return path, nil
}

func peclFileReferencedByOtherPackage(state peclInstallState, packageName, path string) bool {
	for name, record := range state.Packages {
		if name == packageName {
			continue
		}
		if peclRecordContainsPath(record.Files, path) {
			return true
		}
	}

	return false
}

func peclRecordContainsPath(files []peclInstalledFile, path string) bool {
	for _, file := range files {
		if strings.EqualFold(filepath.Clean(file.Path), filepath.Clean(path)) {
			return true
		}
	}

	return false
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func copyPECLArtifact(source, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary, err := os.CreateTemp(filepath.Dir(target), "pecl-artifact-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := io.Copy(temporary, input); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	_ = os.Remove(target)
	if err := os.Rename(temporaryPath, target); err != nil {
		return fmt.Errorf("install PECL artifact %s: %w; stop the environment and retry", target, err)
	}

	return nil
}

// installPECLArtifact prevents a package from overwriting files it does not
// own. Identical companion DLLs already tracked by another package may be
// shared; an identical untracked runtime file is left untouched and untracked.
func installPECLArtifact(source, target, installRoot string, state peclInstallState, packageName string, allowShared bool) (bool, error) {
	relative, err := filepath.Rel(installRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return false, fmt.Errorf("PECL artifact %s is outside PHP install root", target)
	}
	relative = filepath.ToSlash(relative)
	_, statErr := os.Stat(target)
	if errors.Is(statErr, os.ErrNotExist) {
		return true, copyPECLArtifact(source, target)
	}
	if statErr != nil {
		return false, statErr
	}
	ownedByPackage := peclRecordContainsPath(state.Packages[packageName].Files, relative)
	ownedByOther := peclFileReferencedByOtherPackage(state, packageName, relative)
	if ownedByPackage {
		return true, copyPECLArtifact(source, target)
	}
	sourceHash, err := fileSHA256(source)
	if err != nil {
		return false, err
	}
	targetHash, err := fileSHA256(target)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(sourceHash, targetHash) {
		return false, fmt.Errorf("refusing to overwrite unowned PECL artifact %s", target)
	}
	if allowShared && ownedByOther {
		return true, nil
	}
	if allowShared {
		return false, nil
	}

	return false, fmt.Errorf("refusing to adopt untracked PECL module %s", target)
}

func extractPECLZip(archive, target string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, entry := range reader.File {
		path, err := safePECLArchivePath(target, entry.Name)
		if err != nil {
			return err
		}
		if entry.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		source, err := entry.Open()
		if err != nil {
			return err
		}
		targetFile, err := os.Create(path)
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(targetFile, source)
		source.Close()
		targetFile.Close()
		if copyErr != nil {
			return copyErr
		}
	}

	return nil
}

func extractPECLTarGz(archive, target string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := safePECLArchivePath(target, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			targetFile, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, header.FileInfo().Mode())
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(targetFile, reader)
			targetFile.Close()
			if copyErr != nil {
				return copyErr
			}
		}
	}
}

func safePECLArchivePath(root, entry string) (string, error) {
	cleanRoot := filepath.Clean(root)
	path := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(entry)))
	if path != cleanRoot && !strings.HasPrefix(path, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("PECL archive entry %q escapes staging directory", entry)
	}

	return path, nil
}

func findPECLFile(root, name string) (string, error) {
	var match string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.EqualFold(entry.Name(), name) {
			match = path
			return filepath.SkipAll
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if match == "" {
		return "", os.ErrNotExist
	}

	return match, nil
}

func findPECLBuildRoot(root string) (string, error) {
	configM4, err := findPECLFile(root, "config.m4")
	if err != nil {
		return "", fmt.Errorf("PECL source archive has no config.m4: %w", err)
	}

	return filepath.Dir(configM4), nil
}

func verifyPECLPackageFiles(root string, directory peclPackageDir) error {
	var verify func(peclPackageDir) error
	verify = func(current peclPackageDir) error {
		for _, file := range current.Files {
			if strings.TrimSpace(file.MD5) == "" {
				continue
			}
			path, err := safePECLArchivePath(root, file.Name)
			if err != nil {
				return err
			}
			input, err := os.Open(path)
			if err != nil {
				return fmt.Errorf("verify PECL package file %s: %w", file.Name, err)
			}
			hasher := md5.New() // package.xml publishes MD5 per source file.
			_, copyErr := io.Copy(hasher, input)
			input.Close()
			if copyErr != nil {
				return copyErr
			}
			if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), file.MD5) {
				return fmt.Errorf("PECL package checksum mismatch for %s", file.Name)
			}
		}
		for _, child := range current.Dirs {
			if err := verify(child); err != nil {
				return err
			}
		}
		return nil
	}

	return verify(directory)
}

func resolvePECLBuildTools(installRoot, phpPath string) (string, string, error) {
	return resolvePHPBuildTools(installRoot, phpPath, "legacy PECL Linux builds")
}

// resolvePHPBuildTools verifies phpize/php-config match the target PHP and
// that the host exposes the standard native-extension build commands.
func resolvePHPBuildTools(installRoot, phpPath, purpose string) (string, string, error) {
	phpize, err := findPECLBuildTool(installRoot, "phpize")
	if err != nil {
		return "", "", fmt.Errorf("%s require phpize matching %s: %w", purpose, phpPath, err)
	}
	phpConfig, err := findPECLBuildTool(installRoot, "php-config")
	if err != nil {
		return "", "", fmt.Errorf("%s require php-config matching %s: %w", purpose, phpPath, err)
	}
	target, err := inspectPECLPHP(phpPath)
	if err != nil {
		return "", "", err
	}
	output, err := exec.Command(phpConfig, "--version").Output()
	if err != nil {
		return "", "", fmt.Errorf("inspect php-config %s for %s: %w", phpConfig, purpose, err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(output)), target.Series+".") && strings.TrimSpace(string(output)) != target.Series {
		return "", "", fmt.Errorf("php-config %s reports PHP %s but target PHP is %s", phpConfig, strings.TrimSpace(string(output)), target.Version)
	}
	for _, tool := range []string{"make", "autoconf"} {
		if _, err := exec.LookPath(tool); err != nil {
			return "", "", fmt.Errorf("%s require %s on PATH", purpose, tool)
		}
	}
	if _, err := exec.LookPath("cc"); err != nil {
		if _, gccErr := exec.LookPath("gcc"); gccErr != nil {
			return "", "", fmt.Errorf("%s require a C compiler (cc or gcc) on PATH", purpose)
		}
	}

	return phpize, phpConfig, nil
}

func findPECLBuildTool(installRoot, name string) (string, error) {
	for _, candidate := range []string{filepath.Join(installRoot, name), filepath.Join(installRoot, "bin", name)} {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}

	return path, nil
}

func runPECLBuildCommand(stdout, stderr io.Writer, directory, target string, args ...string) error {
	command := exec.Command(target, args...)
	command.Dir = directory
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run PECL build command %s: %w", filepath.Base(target), err)
	}

	return nil
}

func (s Store) installPECLExtensions(environment Environment, report func(InstallProgress)) error {
	if len(environment.PECLExtensions) == 0 {
		return nil
	}
	projectPHP, tool, phpVersion, err := s.resolvePECLTargetPHP(environment)
	if err != nil {
		return err
	}
	installRoot := filepath.Join(s.EnvsDir, tool, phpVersion)
	state, err := readPECLInstallState(installRoot)
	if err != nil {
		return err
	}
	modules, err := tools.InstalledPHPModules(projectPHP)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(environment.PECLExtensions))
	for name := range environment.PECLExtensions {
		names = append(names, name)
	}
	sort.Strings(names)
	for index, name := range names {
		extension := environment.PECLExtensions[name]
		progress := InstallProgress{Index: index + 1, Total: len(names), Tool: "pecl:" + name, Version: extension.Version}
		if record, ok := state.Packages[name]; ok && (extension.Version == "*" || record.Version == extension.Version) && modules[record.Module] && peclRecordFilesValid(installRoot, record) {
			emitInstallProgress(report, progress, InstallProgressSkipped)
			continue
		}
		emitInstallProgress(report, progress, InstallProgressInstalling)
		resolution, err := s.resolvePECLPackage(projectPHP, name, extension.Version)
		if err != nil {
			return err
		}
		if err := validatePECLConfigureOptions(resolution, extension.ConfigureOptions); err != nil {
			return err
		}
		if err := rejectPECLPIEConflict(environment, resolution.Module); err != nil {
			return err
		}
		if err := s.installResolvedPECLExtension(io.Discard, io.Discard, projectPHP, tool, phpVersion, resolution, extension.ConfigureOptions); err != nil {
			return fmt.Errorf("install legacy PECL extension %s: %w", name, err)
		}
		emitInstallProgress(report, progress, InstallProgressInstalled)
	}

	return nil
}

func peclRecordFilesValid(installRoot string, record peclInstalledPackage) bool {
	for _, file := range record.Files {
		path, err := peclStateArtifactPath(installRoot, file.Path)
		if err != nil {
			return false
		}
		digest, err := fileSHA256(path)
		if err != nil || !strings.EqualFold(digest, file.SHA256) {
			return false
		}
	}

	return len(record.Files) > 0
}

// withInstalledPECLExtensions folds tracked PECL modules into the runtime-only
// PHP config. Explicit php-extensions false values still win.
func (s Store) withInstalledPECLExtensions(environment Environment) Environment {
	tool, version := config.PrimaryPHPTool(environment)
	if tool == "" || len(environment.PECLExtensions) == 0 {
		return environment
	}
	state, err := readPECLInstallState(filepath.Join(s.EnvsDir, tool, version))
	if err != nil {
		return environment
	}
	if environment.PHPExtensions == nil {
		environment.PHPExtensions = map[string]bool{}
	}
	if environment.ZendExtensions == nil {
		environment.ZendExtensions = map[string]bool{}
	}
	for name := range environment.PECLExtensions {
		record, ok := state.Packages[name]
		if !ok || strings.TrimSpace(record.Module) == "" {
			continue
		}
		if _, explicitlyConfigured := environment.PHPExtensions[record.Module]; !explicitlyConfigured {
			environment.PHPExtensions[record.Module] = true
		}
		if record.Zend {
			environment.ZendExtensions[record.Module] = true
		}
	}

	return environment
}

func (s Store) syncPECLRuntimeConfig(environment Environment) error {
	tool, version := config.PrimaryPHPTool(environment)
	if tool == "" {
		return nil
	}

	return tools.ResyncInstalledPHPRuntimeConfig(s.EnvsDir, tool, version, s.withFrameworkPHPConfig(s.withInstalledPECLExtensions(environment)))
}
