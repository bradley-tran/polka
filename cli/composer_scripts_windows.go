package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	manifestPath := composerManifestPath(goos, composerDir, env)
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
