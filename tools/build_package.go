package tools

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// relocatedPrefixVariable is the shell variable relocated scripts assign their
// computed install root to.
const relocatedPrefixVariable = "polka_prefix"

// singleQuotedPrefixAssignment matches an assignment such as prefix='/opt/x',
// whose value must become double-quoted for the computed prefix to expand.
var singleQuotedPrefixAssignment = regexp.MustCompile(`(?m)^([A-Za-z_][A-Za-z0-9_]*)='([^'\n]*)'[ \t]*$`)

// applyBuildPackage shapes an installed tree into the payload the recipe
// declares. sourceDir is the final stage's source tree and prefix is the path
// compiled into the tool.
func applyBuildPackage(spec manifestBuildPackage, sourceDir, payloadDir, prefix string) error {
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		return fmt.Errorf("create payload directory: %w", err)
	}
	for _, entry := range spec.Copy {
		if err := copyBuildFile(buildPackagePath(sourceDir, entry.From), buildPackagePath(payloadDir, entry.To)); err != nil {
			return err
		}
	}
	for _, relative := range spec.Remove {
		if err := os.RemoveAll(buildPackagePath(payloadDir, relative)); err != nil {
			return fmt.Errorf("remove payload path %s: %w", relative, err)
		}
	}
	for _, relative := range spec.KeepDirs {
		if err := os.MkdirAll(buildPackagePath(payloadDir, relative), 0o755); err != nil {
			return fmt.Errorf("create payload directory %s: %w", relative, err)
		}
	}
	for _, relative := range spec.RelocateScripts {
		if err := relocateBuildScript(buildPackagePath(payloadDir, relative), relative, prefix); err != nil {
			return err
		}
	}

	return nil
}

// copyBuildFile copies a regular file, keeping its permission bits.
func copyBuildFile(sourcePath, targetPath string) (err error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return fmt.Errorf("open built file: %w", err)
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return fmt.Errorf("stat built file %s: %w", sourcePath, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("built file %s is not a regular file", sourcePath)
	}
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", targetPath, err)
	}
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create payload file %s: %w", targetPath, err)
	}
	defer func() {
		if closeErr := target.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close payload file %s: %w", targetPath, closeErr)
		}
	}()
	if _, err := io.Copy(target, source); err != nil {
		return fmt.Errorf("copy built file %s: %w", sourcePath, err)
	}

	return nil
}

// relocateBuildScript rewrites a shell script so it finds its install root from
// its own location. Payloads extract into per-project directories, so the path
// compiled in at build time never exists at runtime; php-config and phpize would
// otherwise send PECL and PIE to a directory that is not there.
func relocateBuildScript(scriptPath, relative, prefix string) error {
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("read script to relocate: %w", err)
	}
	info, err := os.Stat(scriptPath)
	if err != nil {
		return fmt.Errorf("stat script to relocate %s: %w", scriptPath, err)
	}
	relocated, err := relocateShellScript(string(data), relative, prefix)
	if err != nil {
		return fmt.Errorf("relocate %s: %w", relative, err)
	}
	if err := os.WriteFile(scriptPath, []byte(relocated), info.Mode().Perm()); err != nil {
		return fmt.Errorf("write relocated script %s: %w", scriptPath, err)
	}

	return nil
}

// relocateShellScript returns the script text with each literal prefix replaced
// by a variable computed from the script's location.
func relocateShellScript(text, relative, prefix string) (string, error) {
	if !strings.HasPrefix(text, "#!") {
		return "", fmt.Errorf("not a shell script")
	}
	if !strings.Contains(text, prefix) {
		return "", fmt.Errorf("does not reference build prefix %s", prefix)
	}
	shebang, body, found := strings.Cut(text, "\n")
	if !found {
		return "", fmt.Errorf("has no body")
	}

	// Single quotes would stop the variable expanding, so an assignment holding
	// the prefix is double-quoted first. Shell metacharacters in such a value
	// would change meaning, and these are plain paths, so refuse anything else.
	var conversionErr error
	body = singleQuotedPrefixAssignment.ReplaceAllStringFunc(body, func(match string) string {
		parts := singleQuotedPrefixAssignment.FindStringSubmatch(match)
		if !strings.Contains(parts[2], prefix) {
			return match
		}
		if strings.ContainsAny(parts[2], "\"$`\\") {
			conversionErr = fmt.Errorf("cannot convert assignment %s", parts[1])
			return match
		}
		return parts[1] + `="` + parts[2] + `"`
	})
	if conversionErr != nil {
		return "", conversionErr
	}
	body = strings.ReplaceAll(body, prefix, "${"+relocatedPrefixVariable+"}")

	// One ".." per directory between the script and the install root. readlink
	// resolves the shims and symlinks a script may be reached through.
	depth := strings.Count(path.Clean(filepath.ToSlash(relative)), "/")
	up := strings.TrimSuffix(strings.Repeat("../", depth), "/")
	if up == "" {
		up = "."
	}
	assignment := fmt.Sprintf(`%s=$(CDPATH= cd -- "$(dirname -- "$(readlink -f -- "$0")")/%s" && pwd)`, relocatedPrefixVariable, up)

	return shebang + "\n" + assignment + "\n" + body, nil
}

// packBuildPayload writes payloadDir to a gzip tar under a single top-level
// directory, which the installer flattens. The wrapper makes that flattening
// predictable: a payload whose only entry is bin/ would otherwise lose it.
func packBuildPayload(payloadDir, topLevel, prefix, archivePath string) (err error) {
	file, err := os.Create(archivePath)
	if err != nil {
		return fmt.Errorf("create payload archive: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close payload archive: %w", closeErr)
		}
	}()

	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	walkErr := filepath.WalkDir(payloadDir, func(entryPath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(payloadDir, entryPath)
		if err != nil {
			return fmt.Errorf("resolve payload path %s: %w", entryPath, err)
		}
		return writeBuildPayloadEntry(tarWriter, entryPath, relative, topLevel, prefix)
	})
	if walkErr != nil {
		return fmt.Errorf("package payload: %w", walkErr)
	}
	if err := tarWriter.Close(); err != nil {
		return fmt.Errorf("finish payload tar: %w", err)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("finish payload gzip: %w", err)
	}

	return nil
}

// writeBuildPayloadEntry appends one file, directory, or symlink. Ownership is
// cleared and modes are masked so a payload never carries the builder's uid or a
// setuid bit, and directories stay traversable so a project can remove them.
func writeBuildPayloadEntry(writer *tar.Writer, entryPath, relative, topLevel, prefix string) error {
	info, err := os.Lstat(entryPath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", entryPath, err)
	}

	name := topLevel
	if relative != "." {
		name = path.Join(topLevel, filepath.ToSlash(relative))
	}
	linkTarget := ""
	switch {
	case info.Mode().IsRegular(), info.IsDir():
	case info.Mode()&os.ModeSymlink != 0:
		linkTarget, err = packagedSymlinkTarget(entryPath, relative, prefix)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("%s has unsupported file type %s", relative, info.Mode().Type())
	}

	header, err := tar.FileInfoHeader(info, linkTarget)
	if err != nil {
		return fmt.Errorf("create tar header for %s: %w", relative, err)
	}
	header.Name = name
	header.Uid, header.Gid = 0, 0
	header.Uname, header.Gname = "", ""
	header.Mode = int64(info.Mode().Perm())
	if info.IsDir() {
		header.Name += "/"
		header.Mode |= 0o700
	}
	if err := writer.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header for %s: %w", relative, err)
	}
	if !info.Mode().IsRegular() {
		return nil
	}

	file, err := os.Open(entryPath)
	if err != nil {
		return fmt.Errorf("open %s: %w", entryPath, err)
	}
	defer file.Close()
	if _, err := io.Copy(writer, file); err != nil {
		return fmt.Errorf("write %s to payload: %w", relative, err)
	}

	return nil
}

// packagedSymlinkTarget returns a symlink's target as a path relative to the
// link. make install often writes absolute links into the build prefix, which
// would dangle after relocation and which the extractor's containment check does
// not catch, so those are rewritten and anything leaving the payload is refused.
func packagedSymlinkTarget(entryPath, relative, prefix string) (string, error) {
	target, err := os.Readlink(entryPath)
	if err != nil {
		return "", fmt.Errorf("read symlink %s: %w", relative, err)
	}

	slashRelative := filepath.ToSlash(relative)
	linkDir := path.Dir(slashRelative)
	slashTarget := filepath.ToSlash(target)
	var resolved string
	if path.IsAbs(slashTarget) {
		cleanPrefix := path.Clean(filepath.ToSlash(prefix))
		cleanTarget := path.Clean(slashTarget)
		if cleanTarget != cleanPrefix && !strings.HasPrefix(cleanTarget, cleanPrefix+"/") {
			return "", fmt.Errorf("symlink %s points outside the payload: %s", relative, target)
		}
		resolved = strings.TrimPrefix(strings.TrimPrefix(cleanTarget, cleanPrefix), "/")
		if resolved == "" {
			resolved = "."
		}
	} else {
		resolved = path.Join(linkDir, slashTarget)
		if resolved == ".." || strings.HasPrefix(resolved, "../") {
			return "", fmt.Errorf("symlink %s points outside the payload: %s", relative, target)
		}
	}

	rewritten, err := filepath.Rel(filepath.FromSlash(linkDir), filepath.FromSlash(resolved))
	if err != nil {
		return "", fmt.Errorf("rewrite symlink %s: %w", relative, err)
	}

	return filepath.ToSlash(rewritten), nil
}

// buildLogTail returns the last lines of a build log, for failure messages.
func buildLogTail(logPath string, lines int) (string, error) {
	data, err := os.ReadFile(logPath)
	if err != nil {
		return "", fmt.Errorf("read build log: %w", err)
	}
	trimmed := bytes.TrimRight(data, "\n")
	if len(trimmed) == 0 {
		return "", nil
	}
	all := strings.Split(string(trimmed), "\n")
	if len(all) > lines {
		all = all[len(all)-lines:]
	}

	return strings.Join(all, "\n"), nil
}
