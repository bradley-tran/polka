package tools

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSourceTarball writes a tar.gz shaped like a git-archive release: a pax
// global header first, then a directory and a file with an old modification time.
func writeSourceTarball(t *testing.T, modTime time.Time) string {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "source.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatalf("Create(archive) error = %v", err)
	}
	defer file.Close()
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)

	headers := []struct {
		header   *tar.Header
		contents string
	}{
		{header: &tar.Header{Typeflag: tar.TypeXGlobalHeader, Name: "pax_global_header", PAXRecords: map[string]string{"comment": "0123456789abcdef"}}},
		{header: &tar.Header{Typeflag: tar.TypeDir, Name: "redis-8.2.1/", Mode: 0o755, ModTime: modTime}},
		{header: &tar.Header{Typeflag: tar.TypeReg, Name: "redis-8.2.1/configure", Mode: 0o755, ModTime: modTime}, contents: "#!/bin/sh\n"},
	}
	for _, entry := range headers {
		entry.header.Size = int64(len(entry.contents))
		if err := tarWriter.WriteHeader(entry.header); err != nil {
			t.Fatalf("WriteHeader(%s) error = %v", entry.header.Name, err)
		}
		if _, err := tarWriter.Write([]byte(entry.contents)); err != nil {
			t.Fatalf("Write(%s) error = %v", entry.header.Name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("Close(tar) error = %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("Close(gzip) error = %v", err)
	}

	return archivePath
}

// TestExtractArchiveSkipsPAXGlobalHeader verifies a git-archive tarball such as
// the Redis release extracts instead of failing on its leading metadata entry.
func TestExtractArchiveSkipsPAXGlobalHeader(t *testing.T) {
	archivePath := writeSourceTarball(t, time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC))
	targetDir := t.TempDir()

	if err := extractArchive(archivePath, targetDir, archiveFormatTarGz); err != nil {
		t.Fatalf("extractArchive() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "redis-8.2.1", "configure")); err != nil {
		t.Fatalf("Stat(configure) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(targetDir, "pax_global_header")); !os.IsNotExist(err) {
		t.Fatalf("Stat(pax_global_header) error = %v, want not-exist", err)
	}
}

// TestExtractSourceArchivePreservesModTimes verifies source extraction restores
// file and directory times, so make does not try to regenerate shipped files.
func TestExtractSourceArchivePreservesModTimes(t *testing.T) {
	modTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	archivePath := writeSourceTarball(t, modTime)
	targetDir := t.TempDir()

	if err := extractSourceArchive(archivePath, targetDir, archiveFormatTarGz); err != nil {
		t.Fatalf("extractSourceArchive() error = %v", err)
	}
	for _, name := range []string{"redis-8.2.1", filepath.Join("redis-8.2.1", "configure")} {
		info, err := os.Stat(filepath.Join(targetDir, name))
		if err != nil {
			t.Fatalf("Stat(%s) error = %v", name, err)
		}
		if !info.ModTime().Equal(modTime) {
			t.Fatalf("%s modification time = %v, want %v", name, info.ModTime(), modTime)
		}
	}
}

// TestExtractArchiveDoesNotPreserveModTimes pins payload behavior: tool
// payloads keep install-time timestamps, so only source extraction changes.
func TestExtractArchiveDoesNotPreserveModTimes(t *testing.T) {
	modTime := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	archivePath := writeSourceTarball(t, modTime)
	targetDir := t.TempDir()

	if err := extractArchive(archivePath, targetDir, archiveFormatTarGz); err != nil {
		t.Fatalf("extractArchive() error = %v", err)
	}
	info, err := os.Stat(filepath.Join(targetDir, "redis-8.2.1", "configure"))
	if err != nil {
		t.Fatalf("Stat(configure) error = %v", err)
	}
	if info.ModTime().Equal(modTime) {
		t.Fatalf("configure modification time = %v, want install time", info.ModTime())
	}
}

// TestExtractSourceArchiveRejectsZip verifies zip sources fail clearly, because
// the mtime guarantee is only implemented for tar streams.
func TestExtractSourceArchiveRejectsZip(t *testing.T) {
	if err := extractSourceArchive("unused.zip", t.TempDir(), archiveFormatZip); err == nil {
		t.Fatal("extractSourceArchive(zip) error = nil, want unsupported format")
	}
}
