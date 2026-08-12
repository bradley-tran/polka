package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// linuxComposerScriptSupport tracks temporary executable-permission grants for
// Composer script targets. The kernel honors a PHP shebang line directly, but
// execution fails with "permission denied" when the target file lacks the
// executable bit, which is easily lost across archive extraction, container
// image copies, or a checkout that doesn't preserve file mode.
type linuxComposerScriptSupport struct {
	restored []linuxComposerScriptModeRestore
}

// linuxComposerScriptModeRestore remembers a script's original permissions so
// a temporarily granted executable bit can be removed once Composer finishes.
type linuxComposerScriptModeRestore struct {
	path         string
	originalMode os.FileMode
}

// executeTargetWithComposerScriptSupport grants execute permission to
// extensionless PHP-shebang Composer script targets before running the
// target, and restores their original permissions afterward even when
// execution fails.
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

	support, err := prepareLinuxComposerScriptSupport(goos, workingDir, args, env)
	if err != nil {
		return 0, err
	}

	exitCode, runErr := executeTargetWithEnv(stdout, stderr, env, target, args)
	cleanupErr := support.cleanup()
	return exitCode, errors.Join(runErr, cleanupErr)
}

// prepareLinuxComposerScriptSupport reads the active composer.json and grants
// execute permission to direct extensionless PHP-shebang script commands that
// currently lack it.
func prepareLinuxComposerScriptSupport(goos, workingDir string, args, env []string) (linuxComposerScriptSupport, error) {
	support := linuxComposerScriptSupport{}
	if goos != "linux" {
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
		key := filepath.Clean(target)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}

		if grantErr := support.grantExecutePermission(target); grantErr != nil {
			cleanupErr := support.cleanup()
			return linuxComposerScriptSupport{}, errors.Join(grantErr, cleanupErr)
		}
	}

	return support, nil
}

// grantExecutePermission adds the executable bit to target when it is
// missing, recording the original mode so cleanup can restore it.
func (support *linuxComposerScriptSupport) grantExecutePermission(target string) error {
	info, err := os.Stat(target)
	if err != nil {
		return fmt.Errorf("stat Composer script target %q: %w", target, err)
	}

	mode := info.Mode()
	if mode&0o111 != 0 {
		return nil
	}

	if err := os.Chmod(target, mode|0o111); err != nil {
		return fmt.Errorf("grant execute permission to Composer script target %q: %w", target, err)
	}

	support.restored = append(support.restored, linuxComposerScriptModeRestore{path: target, originalMode: mode})
	return nil
}

// cleanup restores the original permissions of every script target that was
// temporarily made executable.
func (support linuxComposerScriptSupport) cleanup() error {
	var cleanupErr error
	for _, restore := range support.restored {
		if err := os.Chmod(restore.path, restore.originalMode); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("restore permissions for temporary Composer script target %q: %w", restore.path, err))
		}
	}

	return cleanupErr
}
