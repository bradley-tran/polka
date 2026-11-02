package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Host inspection seams, replaced in tests so requirement checks and argument
// detection run without a compiler.
var (
	buildLookPath    = exec.LookPath
	buildHostCommand = runBuildHostCommand
	buildStatPath    = os.Stat
	buildLstatPath   = os.Lstat
)

// runBuildHostCommand runs a short host inspection command and returns its
// combined output.
func runBuildHostCommand(ctx context.Context, name string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()

	return output.String(), err
}

// MissingBuildLibrary names a host development library a recipe needs.
type MissingBuildLibrary struct {
	Name string
	Hint string
	// SkipWith lists --configure-arg values that remove the requirement.
	SkipWith []string
}

// MissingHostRequirementsError lists every host prerequisite absent for a
// build, so a user can install them all in one pass.
type MissingHostRequirementsError struct {
	Tool      string
	Commands  []string
	Libraries []MissingBuildLibrary
}

// Error renders the missing items with install hints and opt-outs.
func (e *MissingHostRequirementsError) Error() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "cannot build %s: missing host requirements", e.Tool)
	if len(e.Commands) > 0 {
		fmt.Fprintf(&builder, "\n  commands: %s", strings.Join(e.Commands, ", "))
	}
	for _, library := range e.Libraries {
		fmt.Fprintf(&builder, "\n  library: %s", library.Name)
		if library.Hint != "" {
			fmt.Fprintf(&builder, " (%s)", library.Hint)
		}
		if len(library.SkipWith) > 0 {
			fmt.Fprintf(&builder, "; or opt out with --configure-arg=%s", library.SkipWith[0])
		}
	}

	return builder.String()
}

// checkBuildHostRequirements verifies the recipe's prerequisites, gathering
// every missing item instead of stopping at the first.
func checkBuildHostRequirements(ctx context.Context, tool string, requires manifestBuildRequires, configureArgs []string) error {
	missing := &MissingHostRequirementsError{Tool: tool}
	for _, command := range requires.Commands {
		if !buildCommandAvailable(command) {
			missing.Commands = append(missing.Commands, strings.ReplaceAll(command, "|", " or "))
		}
	}
	for _, library := range requires.Libraries {
		if buildLibrarySkipped(library, configureArgs) || buildLibraryAvailable(ctx, library) {
			continue
		}
		missing.Libraries = append(missing.Libraries, MissingBuildLibrary{
			Name:     strings.TrimSpace(library.Name),
			Hint:     strings.TrimSpace(library.Hint),
			SkipWith: library.SkipWith,
		})
	}
	if len(missing.Commands) > 0 || len(missing.Libraries) > 0 {
		return missing
	}

	return nil
}

// buildCommandAvailable reports whether any "a|b" alternative is on PATH.
func buildCommandAvailable(command string) bool {
	for _, alternative := range strings.Split(command, "|") {
		if _, err := buildLookPath(strings.TrimSpace(alternative)); err == nil {
			return true
		}
	}

	return false
}

// buildLibrarySkipped reports whether a --configure-arg opts out of library.
func buildLibrarySkipped(library manifestBuildLibrary, configureArgs []string) bool {
	for _, skip := range library.SkipWith {
		for _, arg := range configureArgs {
			if strings.TrimSpace(skip) == arg {
				return true
			}
		}
	}

	return false
}

// buildLibraryAvailable reports whether pkg-config knows the library or the
// compiler can find its header. Failed probes only mean "not available".
func buildLibraryAvailable(ctx context.Context, library manifestBuildLibrary) bool {
	if name := strings.TrimSpace(library.PkgConfig); name != "" {
		if _, err := buildHostCommand(ctx, "pkg-config", "--exists", name); err == nil {
			return true
		}
	}
	if header := strings.TrimSpace(library.Header); header != "" {
		compiler := buildHostCompiler()
		if compiler == "" {
			return false
		}
		if _, err := buildHostCommand(ctx, compiler, "-E", "-include", header, "-x", "c", os.DevNull); err == nil {
			return true
		}
	}

	return false
}

// buildHostCompiler returns the C compiler on PATH, or "" when there is none.
func buildHostCompiler() string {
	for _, candidate := range []string{"cc", "gcc"} {
		if _, err := buildLookPath(candidate); err == nil {
			return candidate
		}
	}

	return ""
}

// describeBuildHostCompiler returns the compiler's version line for build
// metadata. Metadata is informational, so a failed probe records nothing.
func describeBuildHostCompiler(ctx context.Context) string {
	compiler := buildHostCompiler()
	if compiler == "" {
		return ""
	}
	output, err := buildHostCommand(ctx, compiler, "--version")
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(output, "\n")

	return strings.TrimSpace(line)
}

// detectBuildLibdir returns the value for PHP's --with-libdir, or "" when the
// default lib directory is correct. Debian-style hosts keep libraries under a
// multiarch triple, RPM-style hosts under a real lib64. Arch makes /usr/lib64 a
// symlink to lib, which must not count, so the symlink check matters.
func detectBuildLibdir(ctx context.Context) string {
	if compiler := buildHostCompiler(); compiler != "" {
		output, err := buildHostCommand(ctx, compiler, "-dumpmachine")
		if triple := strings.TrimSpace(output); err == nil && triple != "" && !strings.ContainsAny(triple, "/ \n") {
			if info, statErr := buildStatPath("/usr/lib/" + triple); statErr == nil && info.IsDir() {
				return "lib/" + triple
			}
		}
	}
	if info, err := buildLstatPath("/usr/lib64"); err == nil && info.IsDir() {
		return "lib64"
	}

	return ""
}

// sanitizedBuildEnv returns the environment for compile commands: variables
// that redirect installs or change make's behavior are removed, PHP settings
// are dropped so a project's ini cannot affect PHP's own build, Polka's shims
// leave PATH so a build cannot find the tool it is replacing, and the locale is
// fixed so compiler output stays searchable in the log.
func sanitizedBuildEnv(base []string) []string {
	removed := map[string]bool{
		"MAKEFLAGS":        true,
		"MFLAGS":           true,
		"MAKELEVEL":        true,
		"DESTDIR":          true,
		"INSTALL_ROOT":     true,
		"CONFIG_SITE":      true,
		"PHPRC":            true,
		"PHP_INI_SCAN_DIR": true,
		"LC_ALL":           true,
	}

	env := make([]string, 0, len(base)+1)
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if !found || removed[name] {
			continue
		}
		if name == "PATH" {
			entry = "PATH=" + withoutPolkaShims(value)
		}
		env = append(env, entry)
	}

	return append(env, "LC_ALL=C")
}

// withoutPolkaShims removes project shim directories from a PATH value.
func withoutPolkaShims(path string) string {
	separator := string(os.PathListSeparator)
	kept := make([]string, 0)
	for _, entry := range strings.Split(path, separator) {
		normalized := strings.TrimRight(strings.ReplaceAll(entry, `\`, "/"), "/")
		if strings.HasSuffix(normalized, "/.polka/bin") {
			continue
		}
		kept = append(kept, entry)
	}

	return strings.Join(kept, separator)
}
