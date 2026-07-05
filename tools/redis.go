package tools

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"polka/config"
)

const (
	redisServerPackageName  = "redis-server"
	redisToolsPackageName   = "redis-tools"
	redisWindowsGitHubOwner = "zkteco-home"
	redisWindowsGitHubRepo  = "redis-windows"
)

var (
	redisAPTBaseURL       = "https://packages.redis.io/deb"
	redisAPTDistributions = []string{"jammy", "bookworm"}
	redisAPTPackagesURL   = func(distribution string) string {
		return strings.TrimRight(redisAPTBaseURL, "/") + "/dists/" + strings.TrimSpace(distribution) + "/main/binary-amd64/Packages"
	}
	redisWindowsArchiveURL = func(tag string) string {
		return fmt.Sprintf("https://github.com/%s/%s/archive/refs/tags/%s.zip", redisWindowsGitHubOwner, redisWindowsGitHubRepo, strings.TrimSpace(tag))
	}
	redisRuntimeGOOS   = func() string { return runtime.GOOS }
	redisRuntimeGOARCH = func() string { return runtime.GOARCH }
)

type redisDebianPackage struct {
	Name         string
	Version      string
	Upstream     string
	Filename     string
	SHA256       string
	Distribution string
}

func redisPlugin() Plugin {
	return newManifestPlugin(Redis, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateRedisConfig(environment.Redis)
		},
		download: func(ctx DownloadContext) error {
			return downloadRedis(ctx.Client, ctx.CacheDir, ctx.Version)
		},
	})
}

func validateRedisConfig(redis *config.RedisConfig) error {
	if redis == nil {
		return nil
	}
	if strings.TrimSpace(redis.Version) == "" {
		return fmt.Errorf("redis configuration requires version")
	}
	if err := validateVersion(Redis, redis.Version); err != nil {
		return err
	}
	if redis.Port != 0 && !validPort(redis.Port) {
		return fmt.Errorf("redis port must be between 1 and 65535")
	}

	return nil
}

func downloadRedis(client *http.Client, cacheDir, version string) error {
	goos, goarch := redisRuntimeGOOS(), redisRuntimeGOARCH()
	if goos == "linux" && goarch == "amd64" {
		return downloadRedisLinux(client, cacheDir, version)
	}
	if goos == "windows" && goarch == "amd64" {
		return downloadRedisWindows(client, cacheDir, version)
	}

	return fmt.Errorf("redis automatic downloads are supported only on linux/amd64 and windows/amd64; %s/%s is not supported", goos, goarch)
}

func downloadRedisLinux(client *http.Client, cacheDir, version string) error {
	resolvedVersion, serverPackage, toolsPackage, err := resolveRedisAPTAssets(client, version)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Join(cacheDir, Redis), 0o755); err != nil {
		return fmt.Errorf("create redis cache dir: %w", err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, Redis), strings.TrimSpace(version)+"-tmp-")
	if err != nil {
		return fmt.Errorf("create redis staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	payloadDir := filepath.Join(stagingDir, "payload")
	serverPath := filepath.Join(stagingDir, filepath.Base(serverPackage.Filename))
	toolsPath := filepath.Join(stagingDir, filepath.Base(toolsPackage.Filename))
	if err := downloadRedisDebianPackage(client, serverPackage, serverPath); err != nil {
		return err
	}
	if err := downloadRedisDebianPackage(client, toolsPackage, toolsPath); err != nil {
		return err
	}
	if err := extractRedisBinaryFromDebianPackage(serverPath, "usr/bin/redis-server", filepath.Join(payloadDir, "bin", "redis-server")); err != nil {
		return err
	}
	if err := extractRedisBinaryFromDebianPackage(toolsPath, "usr/bin/redis-cli", filepath.Join(payloadDir, "bin", "redis-cli")); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(payloadDir, ".polka-redis-payload"), []byte("redis\n"), 0o644); err != nil {
		return fmt.Errorf("write redis payload marker: %w", err)
	}

	archivePath := filepath.Join(stagingDir, fmt.Sprintf("redis-%s-linux-amd64.zip", resolvedVersion))
	if err := writeRedisZipPayload(payloadDir, archivePath); err != nil {
		return err
	}

	asset := downloadAsset{
		FileName:      filepath.Base(archivePath),
		URL:           strings.TrimRight(redisAPTBaseURL, "/"),
		ArchiveFormat: archiveFormatZip,
	}
	_, err = cacheArchivePayload(cacheDir, Redis, version, resolvedVersion, asset, archivePath)
	return err
}

func downloadRedisWindows(client *http.Client, cacheDir, version string) error {
	resolvedVersion, tag, _, err := resolveGitHubReleaseVersion(client, manifestGitHubDownload{
		Owner: redisWindowsGitHubOwner,
		Repo:  redisWindowsGitHubRepo,
	}, version)
	if err != nil {
		return fmt.Errorf("resolve redis windows version %q: %w", version, err)
	}

	if err := os.MkdirAll(filepath.Join(cacheDir, Redis), 0o755); err != nil {
		return fmt.Errorf("create redis cache dir: %w", err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, Redis), strings.TrimSpace(version)+"-tmp-")
	if err != nil {
		return fmt.Errorf("create redis staging dir: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	archiveURL := redisWindowsArchiveURL(tag)
	archivePath := filepath.Join(stagingDir, fmt.Sprintf("redis-windows-%s.zip", resolvedVersion))
	if err := downloadFile(client, archiveURL, archivePath); err != nil {
		return err
	}

	asset := downloadAsset{
		FileName:      filepath.Base(archivePath),
		URL:           archiveURL,
		ArchiveFormat: archiveFormatZip,
	}
	_, err = cacheArchivePayload(cacheDir, Redis, version, resolvedVersion, asset, archivePath)
	return err
}

func resolveRedisAPTAssets(client *http.Client, requestedVersion string) (string, redisDebianPackage, redisDebianPackage, error) {
	requestedVersion = strings.TrimSpace(requestedVersion)
	if requestedVersion == "" {
		return "", redisDebianPackage{}, redisDebianPackage{}, fmt.Errorf("redis version cannot be empty")
	}

	var selected *redisAPTSelection
	for _, distribution := range redisAPTDistributions {
		indexURL := redisAPTPackagesURL(distribution)
		index, err := downloadText(client, indexURL, "redis apt package index")
		if err != nil {
			return "", redisDebianPackage{}, redisDebianPackage{}, fmt.Errorf("resolve redis packages from %s: %w", strings.TrimSpace(distribution), err)
		}
		selection := selectRedisAPTSelection(parseRedisAPTPackages(index, distribution), requestedVersion)
		if selection == nil {
			continue
		}
		if selected == nil || compareVersions(selection.Server.Upstream, selected.Server.Upstream) > 0 {
			selected = selection
		}
	}
	if selected == nil {
		return "", redisDebianPackage{}, redisDebianPackage{}, fmt.Errorf("no redis %s packages found in official Redis APT metadata", requestedVersion)
	}

	return selected.Server.Upstream, selected.Server, selected.Tools, nil
}

type redisAPTSelection struct {
	Server redisDebianPackage
	Tools  redisDebianPackage
}

func parseRedisAPTPackages(index, distribution string) []redisDebianPackage {
	paragraphs := strings.Split(strings.ReplaceAll(index, "\r\n", "\n"), "\n\n")
	packages := make([]redisDebianPackage, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		fields := parseRedisAPTPackageFields(paragraph)
		name := strings.TrimSpace(fields["Package"])
		if name != redisServerPackageName && name != redisToolsPackageName {
			continue
		}
		version := strings.TrimSpace(fields["Version"])
		filename := strings.TrimSpace(fields["Filename"])
		sha256 := strings.TrimSpace(fields["SHA256"])
		if version == "" || filename == "" || sha256 == "" {
			continue
		}
		packages = append(packages, redisDebianPackage{
			Name:         name,
			Version:      version,
			Upstream:     redisUpstreamVersion(version),
			Filename:     filename,
			SHA256:       sha256,
			Distribution: strings.TrimSpace(distribution),
		})
	}

	return packages
}

func parseRedisAPTPackageFields(paragraph string) map[string]string {
	fields := map[string]string{}
	currentKey := ""
	for _, line := range strings.Split(strings.ReplaceAll(paragraph, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
			if currentKey != "" {
				fields[currentKey] += "\n" + strings.TrimSpace(line)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			currentKey = ""
			continue
		}
		currentKey = strings.TrimSpace(key)
		fields[currentKey] = strings.TrimSpace(value)
	}

	return fields
}

func selectRedisAPTSelection(packages []redisDebianPackage, requestedVersion string) *redisAPTSelection {
	servers := map[string]redisDebianPackage{}
	tools := map[string]redisDebianPackage{}
	for _, pkg := range packages {
		if !versionMatchesRequest(pkg.Upstream, requestedVersion) {
			continue
		}
		switch pkg.Name {
		case redisServerPackageName:
			servers[pkg.Version] = pkg
		case redisToolsPackageName:
			tools[pkg.Version] = pkg
		}
	}

	versions := make([]string, 0, len(servers))
	for version := range servers {
		if _, ok := tools[version]; ok {
			versions = append(versions, version)
		}
	}
	sort.Slice(versions, func(left, right int) bool {
		return compareVersions(servers[versions[left]].Upstream, servers[versions[right]].Upstream) > 0
	})
	if len(versions) == 0 {
		return nil
	}

	version := versions[0]
	return &redisAPTSelection{Server: servers[version], Tools: tools[version]}
}

func redisUpstreamVersion(packageVersion string) string {
	version := strings.TrimSpace(packageVersion)
	if _, after, ok := strings.Cut(version, ":"); ok {
		version = after
	}
	if before, _, ok := strings.Cut(version, "-"); ok {
		version = before
	}

	return strings.TrimSpace(version)
}

func downloadRedisDebianPackage(client *http.Client, pkg redisDebianPackage, targetPath string) error {
	if err := downloadFile(client, redisPackageURL(pkg), targetPath); err != nil {
		return err
	}
	if err := verifyFileChecksum(checksumAlgorithmSHA256, pkg.SHA256, targetPath); err != nil {
		return err
	}

	return nil
}

func redisPackageURL(pkg redisDebianPackage) string {
	return strings.TrimRight(redisAPTBaseURL, "/") + "/" + strings.TrimLeft(strings.TrimSpace(pkg.Filename), "/")
}

func extractRedisBinaryFromDebianPackage(debPath, sourcePath, targetPath string) error {
	extractDir, err := os.MkdirTemp(filepath.Dir(debPath), "deb-data-")
	if err != nil {
		return fmt.Errorf("create redis deb extraction dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	if err := extractRedisDebianData(debPath, extractDir); err != nil {
		return err
	}
	if err := copyRedisPayloadFile(filepath.Join(extractDir, filepath.FromSlash(sourcePath)), targetPath); err != nil {
		return err
	}

	return nil
}

func extractRedisDebianData(debPath, targetDir string) error {
	file, err := os.Open(debPath)
	if err != nil {
		return fmt.Errorf("open redis deb %s: %w", debPath, err)
	}
	defer file.Close()

	magic := make([]byte, 8)
	if _, err := io.ReadFull(file, magic); err != nil {
		return fmt.Errorf("read redis deb header %s: %w", debPath, err)
	}
	if string(magic) != "!<arch>\n" {
		return fmt.Errorf("redis deb %s is not an ar archive", debPath)
	}

	for {
		header := make([]byte, 60)
		if _, err := io.ReadFull(file, header); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				break
			}
			return fmt.Errorf("read redis deb member header: %w", err)
		}
		name := strings.TrimSpace(string(header[:16]))
		name = strings.TrimSuffix(name, "/")
		sizeText := strings.TrimSpace(string(header[48:58]))
		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil {
			return fmt.Errorf("parse redis deb member %q size %q: %w", name, sizeText, err)
		}
		memberPath := filepath.Join(targetDir, name)
		switch {
		case strings.HasPrefix(name, "data.tar."):
			if err := os.MkdirAll(filepath.Dir(memberPath), 0o755); err != nil {
				return fmt.Errorf("create redis deb member dir: %w", err)
			}
			target, err := os.OpenFile(memberPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
			if err != nil {
				return fmt.Errorf("create redis deb member %s: %w", memberPath, err)
			}
			_, copyErr := io.CopyN(target, file, size)
			closeErr := target.Close()
			if copyErr != nil {
				return fmt.Errorf("copy redis deb member %s: %w", name, copyErr)
			}
			if closeErr != nil {
				return fmt.Errorf("close redis deb member %s: %w", name, closeErr)
			}
			if size%2 != 0 {
				if _, err := file.Seek(1, io.SeekCurrent); err != nil {
					return fmt.Errorf("skip redis deb member padding: %w", err)
				}
			}

			return extractRedisDataArchive(memberPath, targetDir)
		default:
			if _, err := io.CopyN(io.Discard, file, size); err != nil {
				return fmt.Errorf("skip redis deb member %s: %w", name, err)
			}
			if size%2 != 0 {
				if _, err := file.Seek(1, io.SeekCurrent); err != nil {
					return fmt.Errorf("skip redis deb member padding: %w", err)
				}
			}
		}
	}

	return fmt.Errorf("redis deb %s does not contain data archive", debPath)
}

func extractRedisDataArchive(archivePath, targetDir string) error {
	switch {
	case strings.HasSuffix(archivePath, ".tar.xz"):
		return extractArchive(archivePath, targetDir, archiveFormatTarXz)
	case strings.HasSuffix(archivePath, ".tar.gz"):
		return extractArchive(archivePath, targetDir, archiveFormatTarGz)
	default:
		return fmt.Errorf("redis deb data archive %s uses unsupported compression", filepath.Base(archivePath))
	}
}

func copyRedisPayloadFile(sourcePath, targetPath string) error {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat redis payload source %s: %w", sourcePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("redis payload source %s is a directory", sourcePath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create redis payload dir: %w", err)
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open redis payload source %s: %w", sourcePath, err)
	}
	defer source.Close()
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return fmt.Errorf("create redis payload target %s: %w", targetPath, err)
	}
	defer target.Close()
	if _, err := io.Copy(target, source); err != nil {
		return fmt.Errorf("copy redis payload to %s: %w", targetPath, err)
	}

	return nil
}

func writeRedisZipPayload(sourceDir, archivePath string) error {
	if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return fmt.Errorf("create redis zip parent dir: %w", err)
	}
	file, err := os.OpenFile(archivePath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create redis zip %s: %w", archivePath, err)
	}

	writer := zip.NewWriter(file)
	walkErr := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat redis zip entry %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		relativePath, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return fmt.Errorf("resolve redis zip entry %s: %w", path, err)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return fmt.Errorf("create redis zip header for %s: %w", path, err)
		}
		header.Name = filepath.ToSlash(relativePath)
		header.Method = zip.Deflate
		entryWriter, err := writer.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create redis zip entry %s: %w", header.Name, err)
		}
		source, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open redis zip source %s: %w", path, err)
		}
		_, copyErr := io.Copy(entryWriter, source)
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("write redis zip entry %s: %w", header.Name, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close redis zip source %s: %w", path, closeErr)
		}

		return nil
	})
	if walkErr != nil {
		_ = writer.Close()
		_ = file.Close()
		return walkErr
	}
	if err := writer.Close(); err != nil {
		_ = file.Close()
		return fmt.Errorf("close redis zip writer: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close redis zip %s: %w", archivePath, err)
	}

	return nil
}
