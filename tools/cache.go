package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	cacheMetadataFileName      = "metadata.json"
	cacheMetadataLockFileName  = ".metadata.lock"
	cacheMetadataSchemaVersion = 1
	cacheLockTimeout           = 30 * time.Second
	cacheLockStaleAfter        = 10 * time.Minute
)

type payloadKind string

const (
	payloadKindArchive payloadKind = "archive"
	payloadKindFile    payloadKind = "file"
)

// ErrCacheMiss indicates that a cache entry is absent, incomplete, or invalid.
var ErrCacheMiss = errors.New("cache miss")

type toolCacheMetadata struct {
	SchemaVersion int                         `json:"schemaVersion"`
	Tool          string                      `json:"tool"`
	Versions      map[string]toolCacheVersion `json:"versions"`
}

type toolCacheVersion struct {
	DownloadedVersion string            `json:"downloadedVersion"`
	PayloadKind       payloadKind       `json:"payloadKind"`
	PayloadPath       string            `json:"payloadPath"`
	FileName          string            `json:"fileName"`
	InstallPath       string            `json:"installPath,omitempty"`
	SourceURL         string            `json:"sourceUrl"`
	ArchiveFormat     archiveFormat     `json:"archiveFormat,omitempty"`
	ChecksumAlgorithm checksumAlgorithm `json:"checksumAlgorithm"`
	Checksum          string            `json:"checksum"`
	Size              int64             `json:"size"`
	DownloadedAt      time.Time         `json:"downloadedAt"`
}

// CachedPayload describes a validated cached payload for a requested tool version.
type CachedPayload struct {
	Tool              string
	RequestedVersion  string
	DownloadedVersion string
	PayloadPath       string
	PayloadKind       string
}

type cacheLock struct {
	path string
	file *os.File
}

// IsCacheMiss reports whether err represents an absent or invalid cache entry.
func IsCacheMiss(err error) bool {
	return errors.Is(err, ErrCacheMiss)
}

// CachedToolPayload returns the validated cached payload for a requested tool version.
func CachedToolPayload(cacheDir, tool, version string) (CachedPayload, error) {
	payload, _, err := cachedToolPayload(cacheDir, tool, version)
	return payload, err
}

// InstallCachedToolPayload materializes a validated cached payload into targetDir.
func InstallCachedToolPayload(cacheDir, targetDir, tool, version string) (CachedPayload, error) {
	payload, entry, err := cachedToolPayload(cacheDir, tool, version)
	if err != nil {
		return CachedPayload{}, err
	}

	switch entry.PayloadKind {
	case payloadKindArchive:
		if err := extractArchive(payload.PayloadPath, targetDir, entry.ArchiveFormat); err != nil {
			return CachedPayload{}, err
		}
		if err := collapseSingleDirectory(targetDir); err != nil {
			return CachedPayload{}, err
		}
	case payloadKindFile:
		if strings.TrimSpace(entry.InstallPath) == "" {
			return CachedPayload{}, fmt.Errorf("cache metadata for %s %s does not define installPath", tool, version)
		}
		targetPath, err := safeRelativePath(targetDir, entry.InstallPath)
		if err != nil {
			return CachedPayload{}, fmt.Errorf("cache metadata for %s %s has invalid installPath: %w", tool, version, err)
		}
		if err := copyCachedPayloadFile(payload.PayloadPath, targetPath); err != nil {
			return CachedPayload{}, err
		}
	default:
		return CachedPayload{}, fmt.Errorf("cache metadata for %s %s has unsupported payload kind %q", tool, version, entry.PayloadKind)
	}

	return payload, nil
}

func cacheArchivePayload(cacheDir, tool, requestedVersion, downloadedVersion string, asset downloadAsset, stagingPath string) (string, error) {
	entry := toolCacheVersion{
		DownloadedVersion: strings.TrimSpace(downloadedVersion),
		PayloadKind:       payloadKindArchive,
		FileName:          strings.TrimSpace(asset.FileName),
		SourceURL:         strings.TrimSpace(asset.URL),
		ArchiveFormat:     asset.ArchiveFormat,
		ChecksumAlgorithm: asset.ChecksumAlgorithm,
		Checksum:          strings.TrimSpace(asset.Checksum),
		DownloadedAt:      time.Now().UTC(),
	}

	return cacheDownloadedPayload(cacheDir, tool, requestedVersion, entry, stagingPath)
}

func cacheFilePayload(cacheDir, tool, requestedVersion, downloadedVersion, fileName, sourceURL, installPath string, algorithm checksumAlgorithm, checksum, stagingPath string) (string, error) {
	entry := toolCacheVersion{
		DownloadedVersion: strings.TrimSpace(downloadedVersion),
		PayloadKind:       payloadKindFile,
		FileName:          strings.TrimSpace(fileName),
		InstallPath:       filepath.ToSlash(strings.TrimSpace(installPath)),
		SourceURL:         strings.TrimSpace(sourceURL),
		ChecksumAlgorithm: algorithm,
		Checksum:          strings.TrimSpace(checksum),
		DownloadedAt:      time.Now().UTC(),
	}

	return cacheDownloadedPayload(cacheDir, tool, requestedVersion, entry, stagingPath)
}

func cacheDownloadedPayload(cacheDir, tool, requestedVersion string, entry toolCacheVersion, stagingPath string) (string, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	requestedVersion = strings.TrimSpace(requestedVersion)
	if tool == "" {
		return "", fmt.Errorf("cache tool cannot be empty")
	}
	if requestedVersion == "" {
		return "", fmt.Errorf("%s cache version cannot be empty", tool)
	}
	if strings.TrimSpace(entry.DownloadedVersion) == "" {
		entry.DownloadedVersion = requestedVersion
	}
	if strings.TrimSpace(entry.FileName) == "" {
		return "", fmt.Errorf("%s cache payload filename cannot be empty", tool)
	}
	if filepath.Base(entry.FileName) != entry.FileName {
		return "", fmt.Errorf("%s cache payload filename %q must not contain directories", tool, entry.FileName)
	}

	if entry.ChecksumAlgorithm == checksumAlgorithmNone || strings.TrimSpace(entry.Checksum) == "" {
		checksum, size, err := checksumFile(checksumAlgorithmSHA256, stagingPath)
		if err != nil {
			return "", err
		}
		entry.ChecksumAlgorithm = checksumAlgorithmSHA256
		entry.Checksum = checksum
		entry.Size = size
	} else {
		checksum, _, err := validateChecksumValue(entry.ChecksumAlgorithm, entry.Checksum)
		if err != nil {
			return "", err
		}
		if err := verifyFileChecksum(entry.ChecksumAlgorithm, checksum, stagingPath); err != nil {
			return "", err
		}
		entry.Checksum = checksum
		size, err := fileSize(stagingPath)
		if err != nil {
			return "", err
		}
		entry.Size = size
	}

	toolDir := filepath.Join(cacheDir, tool)
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s cache dir: %w", tool, err)
	}

	lock, err := acquireCacheLock(toolDir)
	if err != nil {
		return "", err
	}
	defer lock.release()

	payloadRelativePath := filepath.ToSlash(filepath.Join(requestedVersion, entry.FileName))
	payloadPath, err := safeRelativePath(toolDir, payloadRelativePath)
	if err != nil {
		return "", err
	}
	payloadDir := filepath.Dir(payloadPath)
	if err := os.RemoveAll(payloadDir); err != nil {
		return "", fmt.Errorf("reset %s cache payload dir: %w", tool, err)
	}
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return "", fmt.Errorf("create %s cache payload dir: %w", tool, err)
	}
	if err := os.Rename(stagingPath, payloadPath); err != nil {
		return "", fmt.Errorf("finalize %s cache payload: %w", tool, err)
	}

	entry.PayloadPath = payloadRelativePath
	metadata := readToolCacheMetadataForUpdate(toolDir, tool)
	metadata.Versions[requestedVersion] = entry
	if err := writeToolCacheMetadata(toolDir, metadata); err != nil {
		return "", err
	}

	return payloadPath, nil
}

func cachedToolPayload(cacheDir, tool, version string) (CachedPayload, toolCacheVersion, error) {
	tool = strings.ToLower(strings.TrimSpace(tool))
	version = strings.TrimSpace(version)
	toolDir := filepath.Join(cacheDir, tool)

	metadata, err := readToolCacheMetadata(toolDir, tool)
	if err != nil {
		return CachedPayload{}, toolCacheVersion{}, err
	}
	entry, ok := metadata.Versions[version]
	if !ok {
		return CachedPayload{}, toolCacheVersion{}, fmt.Errorf("%w: %s %s has no metadata entry", ErrCacheMiss, tool, version)
	}

	payloadPath, err := safeRelativePath(toolDir, entry.PayloadPath)
	if err != nil {
		return CachedPayload{}, toolCacheVersion{}, fmt.Errorf("%w: %s %s has invalid payload path: %v", ErrCacheMiss, tool, version, err)
	}
	if err := validateCachedPayloadFile(payloadPath, entry); err != nil {
		return CachedPayload{}, toolCacheVersion{}, err
	}

	return CachedPayload{
		Tool:              tool,
		RequestedVersion:  version,
		DownloadedVersion: entry.DownloadedVersion,
		PayloadPath:       payloadPath,
		PayloadKind:       string(entry.PayloadKind),
	}, entry, nil
}

func readToolCacheMetadata(toolDir, tool string) (toolCacheMetadata, error) {
	path := filepath.Join(toolDir, cacheMetadataFileName)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return toolCacheMetadata{}, fmt.Errorf("%w: %s metadata is missing", ErrCacheMiss, tool)
	}
	if err != nil {
		return toolCacheMetadata{}, fmt.Errorf("read %s cache metadata: %w", tool, err)
	}

	var metadata toolCacheMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return toolCacheMetadata{}, fmt.Errorf("%w: parse %s cache metadata: %v", ErrCacheMiss, tool, err)
	}
	if metadata.SchemaVersion != cacheMetadataSchemaVersion {
		return toolCacheMetadata{}, fmt.Errorf("%w: %s cache metadata schema version %d is unsupported", ErrCacheMiss, tool, metadata.SchemaVersion)
	}
	if !strings.EqualFold(strings.TrimSpace(metadata.Tool), strings.TrimSpace(tool)) {
		return toolCacheMetadata{}, fmt.Errorf("%w: %s cache metadata is for tool %q", ErrCacheMiss, tool, metadata.Tool)
	}
	if metadata.Versions == nil {
		metadata.Versions = map[string]toolCacheVersion{}
	}

	return metadata, nil
}

func readToolCacheMetadataForUpdate(toolDir, tool string) toolCacheMetadata {
	metadata, err := readToolCacheMetadata(toolDir, tool)
	if err == nil {
		return metadata
	}

	return toolCacheMetadata{
		SchemaVersion: cacheMetadataSchemaVersion,
		Tool:          strings.ToLower(strings.TrimSpace(tool)),
		Versions:      map[string]toolCacheVersion{},
	}
}

func writeToolCacheMetadata(toolDir string, metadata toolCacheMetadata) error {
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		return fmt.Errorf("create cache metadata dir: %w", err)
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache metadata: %w", err)
	}
	data = append(data, '\n')

	targetPath := filepath.Join(toolDir, cacheMetadataFileName)
	tempFile, err := os.CreateTemp(toolDir, cacheMetadataFileName+".tmp-")
	if err != nil {
		return fmt.Errorf("create cache metadata temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if _, err := tempFile.Write(data); err != nil {
		tempFile.Close()
		return fmt.Errorf("write cache metadata temp file: %w", err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close cache metadata temp file: %w", err)
	}
	if err := replaceFile(tempPath, targetPath); err != nil {
		return fmt.Errorf("finalize cache metadata: %w", err)
	}

	return nil
}

func validateCachedPayloadFile(payloadPath string, entry toolCacheVersion) error {
	info, err := os.Stat(payloadPath)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: cached payload %s is missing", ErrCacheMiss, payloadPath)
	}
	if err != nil {
		return fmt.Errorf("stat cached payload %s: %w", payloadPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%w: cached payload %s is a directory", ErrCacheMiss, payloadPath)
	}
	if entry.Size > 0 && info.Size() != entry.Size {
		return fmt.Errorf("%w: cached payload %s size mismatch", ErrCacheMiss, payloadPath)
	}
	if entry.ChecksumAlgorithm == checksumAlgorithmNone || strings.TrimSpace(entry.Checksum) == "" {
		return fmt.Errorf("%w: cached payload %s has no checksum", ErrCacheMiss, payloadPath)
	}
	if err := verifyFileChecksum(entry.ChecksumAlgorithm, entry.Checksum, payloadPath); err != nil {
		return fmt.Errorf("%w: cached payload %s failed checksum validation: %v", ErrCacheMiss, payloadPath, err)
	}

	return nil
}

func checksumFile(algorithm checksumAlgorithm, filePath string) (string, int64, error) {
	newHasher, _, err := checksumHasher(algorithm)
	if err != nil {
		return "", 0, err
	}

	file, err := os.Open(filePath)
	if err != nil {
		return "", 0, fmt.Errorf("open %s for checksum: %w", filePath, err)
	}
	defer file.Close()

	hasher := newHasher()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, fmt.Errorf("checksum %s: %w", filePath, err)
	}

	return fmt.Sprintf("%x", hasher.Sum(nil)), size, nil
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return 0, fmt.Errorf("%s is a directory", path)
	}

	return info.Size(), nil
}

func copyCachedPayloadFile(sourcePath, targetPath string) error {
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return fmt.Errorf("stat cached payload %s: %w", sourcePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", targetPath, err)
	}

	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open cached payload %s: %w", sourcePath, err)
	}
	defer sourceFile.Close()

	targetFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, sourceInfo.Mode())
	if err != nil {
		return fmt.Errorf("create installed payload %s: %w", targetPath, err)
	}
	defer targetFile.Close()

	if _, err := io.Copy(targetFile, sourceFile); err != nil {
		return fmt.Errorf("copy cached payload to %s: %w", targetPath, err)
	}

	return nil
}

// DirLock is an exclusive advisory file lock on a directory, used to
// serialize cross-process work such as global internal tool installs.
type DirLock struct {
	lock *cacheLock
}

// AcquireDirLock takes an exclusive lock file inside dir, creating dir when
// needed. It shares the cache lock's timeout and stale-lock recovery behavior.
func AcquireDirLock(dir string) (*DirLock, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create lock directory %s: %w", dir, err)
	}
	lock, err := acquireCacheLock(dir)
	if err != nil {
		return nil, err
	}

	return &DirLock{lock: lock}, nil
}

// Release removes the lock file; safe to call on a nil lock.
func (l *DirLock) Release() {
	if l == nil {
		return
	}
	l.lock.release()
}

func acquireCacheLock(toolDir string) (*cacheLock, error) {
	lockPath := filepath.Join(toolDir, cacheMetadataLockFileName)
	deadline := time.Now().Add(cacheLockTimeout)
	for {
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n", os.Getpid())
			return &cacheLock{path: lockPath, file: file}, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create cache metadata lock: %w", err)
		}
		removeStaleCacheLock(lockPath)
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timeout waiting for cache metadata lock %s", lockPath)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func removeStaleCacheLock(lockPath string) {
	info, err := os.Stat(lockPath)
	if err != nil {
		return
	}
	if time.Since(info.ModTime()) > cacheLockStaleAfter {
		_ = os.Remove(lockPath)
	}
}

func (l *cacheLock) release() {
	if l == nil {
		return
	}
	if l.file != nil {
		_ = l.file.Close()
	}
	if l.path != "" {
		_ = os.Remove(l.path)
	}
}

func safeRelativePath(root, relativePath string) (string, error) {
	trimmed := strings.TrimSpace(relativePath)
	if trimmed == "" {
		return "", fmt.Errorf("relative path cannot be empty")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("relative path %q must not be absolute", relativePath)
	}

	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(filepath.Join(cleanRoot, filepath.FromSlash(trimmed)))
	if cleanPath != cleanRoot && !strings.HasPrefix(cleanPath, cleanRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("relative path %q escapes root", relativePath)
	}

	return cleanPath, nil
}

func replaceFile(sourcePath, targetPath string) error {
	if err := os.Rename(sourcePath, targetPath); err == nil {
		return nil
	}
	if err := os.Remove(targetPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return os.Rename(sourcePath, targetPath)
}
