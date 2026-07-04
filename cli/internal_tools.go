package cli

import (
	"io"
	"os"
	"runtime"

	"polka/backend"
	"polka/tools"
)

// ensureInternalPHPAndPHAR provisions the internal PHP runtime plus the named
// PHAR tool (composer or pie) into the global tools directory, downloading on
// demand. It returns the php executable, the phar path, and a process
// environment with the internal PHP's runtime config (PHPRC / OPENSSL_CONF)
// applied so the PHAR can make HTTPS requests.
func ensureInternalPHPAndPHAR(stdout io.Writer, store backend.Store, pharTool string) (phpPath, pharPath string, env []string, err error) {
	phpPath, err = ensureInternalTool(stdout, store, tools.PHP)
	if err != nil {
		return "", "", nil, err
	}
	pharPath, err = ensureInternalTool(stdout, store, pharTool)
	if err != nil {
		return "", "", nil, err
	}

	env, err = applyManagedPHPRuntimeConfig(runtime.GOOS, os.Environ(), phpPath)
	if err != nil {
		return "", "", nil, err
	}

	return phpPath, pharPath, env, nil
}

// ensureInternalTool provisions one internal tool, printing progress lines
// for the slow stages so downloads don't look like a hang.
func ensureInternalTool(stdout io.Writer, store backend.Store, tool string) (string, error) {
	return store.EnsureInternalTool(tool, "", internalToolProgress(stdout))
}

// runInternalPHAR executes `<internal-php> <phar> args...` streaming output
// to the caller and returning the process exit code.
func runInternalPHAR(stdout, stderr io.Writer, env []string, phpPath, pharPath string, args []string) (int, error) {
	return executeTargetWithEnv(stdout, stderr, env, phpPath, append([]string{pharPath}, args...))
}
