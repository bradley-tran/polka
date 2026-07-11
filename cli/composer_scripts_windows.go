package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const windowsComposerPHPWrapperMarker = "rem Generated temporarily by Polka for a Composer PHP script."

// windowsComposerScriptSupport tracks temporary wrappers created for Composer
// script commands. Windows does not honor a PHP shebang on an extensionless
// file such as bin/console, so cmd.exe needs a PATHEXT-compatible companion.
type windowsComposerScriptSupport struct {
	created []string
}

// executeTargetWithComposerScriptSupport adds Windows wrappers when the target
// is Composer, executes it, and removes every wrapper even when execution fails.
func executeTargetWithComposerScriptSupport(
	goos string,
	isComposer bool,
	stdout, stderr io.Writer,
	env []string,
	workingDir, target string,
	args []string,
) (int, error) {
	if !isComposer {
		return executeTargetWithEnv(stdout, stderr, env, target, args)
	}

	support, err := prepareWindowsComposerScriptSupport(goos, workingDir, args, env)
	if err != nil {
		return 0, err
	}

	exitCode, runErr := executeTargetWithEnv(stdout, stderr, env, target, args)
	cleanupErr := support.cleanup()
	return exitCode, errors.Join(runErr, cleanupErr)
}

// prepareWindowsComposerScriptSupport reads the active composer.json and writes
// temporary .cmd companions for direct extensionless PHP script commands.
func prepareWindowsComposerScriptSupport(goos, workingDir string, args, env []string) (windowsComposerScriptSupport, error) {
	support := windowsComposerScriptSupport{}
	if goos != "windows" {
		return support, nil
	}

	composerDir := composerCommandWorkingDir(workingDir, args)
	manifestPath := composerManifestPath(composerDir, env)
	data, err := os.ReadFile(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return support, nil
	}
	if err != nil {
		return support, fmt.Errorf("read Composer manifest %q: %w", manifestPath, err)
	}

	commands, err := composerManifestScriptCommands(data)
	if err != nil {
		// Composer owns validation of its manifest. Deferring malformed JSON to
		// Composer preserves its more specific error and normal exit behavior.
		return support, nil
	}

	seen := make(map[string]struct{})
	for _, command := range commands {
		target, ok := composerDirectPHPScriptTarget(composerDir, command)
		if !ok {
			continue
		}
		key := strings.ToLower(filepath.Clean(target))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		created, createErr := support.createWrapper(target)
		if createErr != nil {
			cleanupErr := support.cleanup()
			return windowsComposerScriptSupport{}, errors.Join(createErr, cleanupErr)
		}
		if created != "" {
			support.created = append(support.created, created)
		}
	}

	return support, nil
}

// composerCommandWorkingDir applies Composer's -d/--working-dir option so the
// manifest and referenced script paths are resolved from the same directory.
func composerCommandWorkingDir(workingDir string, args []string) string {
	resolved := workingDir
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "--" {
			break
		}
		var value string
		switch {
		case arg == "-d" || arg == "--working-dir":
			if index+1 < len(args) {
				value = args[index+1]
				index++
			}
		case strings.HasPrefix(arg, "--working-dir="):
			value = strings.TrimPrefix(arg, "--working-dir=")
		case strings.HasPrefix(arg, "-d") && len(arg) > len("-d"):
			value = strings.TrimPrefix(arg, "-d")
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if filepath.IsAbs(value) {
			resolved = value
		} else {
			resolved = filepath.Join(workingDir, value)
		}
	}

	return filepath.Clean(resolved)
}

// composerManifestPath honors COMPOSER when it names an alternate manifest.
func composerManifestPath(workingDir string, env []string) string {
	_, value, ok := lookupEnvValue("windows", env, "COMPOSER")
	value = strings.TrimSpace(value)
	if !ok || value == "" || value == "-" {
		return filepath.Join(workingDir, "composer.json")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}

	return filepath.Join(workingDir, value)
}

// composerManifestScriptCommands extracts strings from scalar and array-valued
// script definitions while ignoring Composer's optional description metadata.
func composerManifestScriptCommands(data []byte) ([]string, error) {
	var manifest struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(manifest.Scripts))
	for name := range manifest.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	var commands []string
	for _, name := range names {
		raw := manifest.Scripts[name]
		var command string
		if err := json.Unmarshal(raw, &command); err == nil {
			commands = append(commands, command)
			continue
		}

		var commandList []string
		if err := json.Unmarshal(raw, &commandList); err == nil {
			commands = append(commands, commandList...)
		}
	}

	return commands, nil
}

// composerDirectPHPScriptTarget returns the first command word when it resolves
// inside the Composer working directory to an extensionless PHP-shebang file.
func composerDirectPHPScriptTarget(workingDir, command string) (string, bool) {
	word := firstComposerScriptWord(command)
	if word == "" || strings.HasPrefix(word, "@") || filepath.Ext(word) != "" {
		return "", false
	}

	target := word
	if !filepath.IsAbs(target) {
		target = filepath.Join(workingDir, filepath.FromSlash(target))
	}
	target = filepath.Clean(target)
	relative, err := filepath.Rel(workingDir, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}

	line, err := readScriptFirstLine(target)
	if err != nil || !isPHPShebang(line) {
		return "", false
	}

	return target, true
}

// firstComposerScriptWord reads one shell-style word and supports the quoting
// used for script paths without attempting to reinterpret the full command.
func firstComposerScriptWord(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}

	var word strings.Builder
	var quote rune
	for _, current := range trimmed {
		switch {
		case quote != 0:
			if current == quote {
				quote = 0
			} else {
				word.WriteRune(current)
			}
		case current == '\'' || current == '"':
			quote = current
		case current == ' ' || current == '\t' || current == '\r' || current == '\n':
			return word.String()
		default:
			word.WriteRune(current)
		}
	}

	return word.String()
}

// createWrapper writes a PATHEXT-compatible companion without replacing a
// project-owned .cmd/.bat file. Stale Polka wrappers are adopted for cleanup.
func (support windowsComposerScriptSupport) createWrapper(target string) (string, error) {
	for _, existing := range []string{target + ".bat", target + ".cmd"} {
		data, err := os.ReadFile(existing)
		if err == nil {
			if strings.Contains(string(data), windowsComposerPHPWrapperMarker) {
				return existing, nil
			}
			return "", nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("inspect Composer script wrapper %q: %w", existing, err)
		}
	}

	wrapperPath := target + ".cmd"
	contents := "@echo off\r\n" +
		windowsComposerPHPWrapperMarker + "\r\n" +
		"call php \"" + escapeWindowsShimValue(target) + "\" %*\r\n" +
		"exit /b %ERRORLEVEL%\r\n"
	file, err := os.OpenFile(wrapperPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if errors.Is(err, os.ErrExist) {
		// A project or concurrent Composer process created the wrapper after the
		// checks above. It owns that file, so this invocation leaves it alone.
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("write Composer PHP script wrapper %q: %w", wrapperPath, err)
	}
	_, writeErr := file.WriteString(contents)
	closeErr := file.Close()
	if writeErr != nil || closeErr != nil {
		removeErr := os.Remove(wrapperPath)
		var wrapperErr error
		if writeErr != nil {
			wrapperErr = errors.Join(wrapperErr, fmt.Errorf("write Composer PHP script wrapper %q: %w", wrapperPath, writeErr))
		}
		if closeErr != nil {
			wrapperErr = errors.Join(wrapperErr, fmt.Errorf("close Composer PHP script wrapper %q: %w", wrapperPath, closeErr))
		}
		if removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			wrapperErr = errors.Join(wrapperErr, fmt.Errorf("remove incomplete Composer PHP script wrapper %q: %w", wrapperPath, removeErr))
		}
		return "", wrapperErr
	}

	return wrapperPath, nil
}

// cleanup removes only wrappers carrying Polka's marker, preserving a file if
// the project replaced it while Composer was running.
func (support windowsComposerScriptSupport) cleanup() error {
	var cleanupErr error
	for _, wrapperPath := range support.created {
		data, err := os.ReadFile(wrapperPath)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("inspect temporary Composer wrapper %q: %w", wrapperPath, err))
			continue
		}
		if !strings.Contains(string(data), windowsComposerPHPWrapperMarker) {
			continue
		}
		if err := os.Remove(wrapperPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove temporary Composer wrapper %q: %w", wrapperPath, err))
		}
	}

	return cleanupErr
}
