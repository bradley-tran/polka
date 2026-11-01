package plugins

import (
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

// dotenvAssignment is one managed dotenv scalar.
type dotenvAssignment struct{ Name, Value string }

// normalizeDatabaseCredentials supplies shared defaults without changing caller data.
func normalizeDatabaseCredentials(credentials *DatabaseCredentials) *DatabaseCredentials {
	if credentials == nil {
		return nil
	}

	normalized := &DatabaseCredentials{
		Engine:       strings.ToLower(strings.TrimSpace(credentials.Engine)),
		Version:      strings.TrimSpace(credentials.Version),
		Host:         strings.TrimSpace(credentials.Host),
		Port:         credentials.Port,
		DatabaseName: strings.TrimSpace(credentials.DatabaseName),
		User:         strings.TrimSpace(credentials.User),
		Password:     credentials.Password,
	}
	if normalized.Host == "" {
		normalized.Host = defaultDatabaseHost
	}
	if normalized.Port == 0 {
		normalized.Port = frameworkDefaultDatabasePort(normalized.Engine)
	}

	return normalized
}

// upsertDotenvAssignments updates managed keys while retaining unrelated lines and comments.
func upsertDotenvAssignments(contents string, assignments []dotenvAssignment) string {
	valueByName := make(map[string]dotenvAssignment, len(assignments))
	for _, assignment := range assignments {
		valueByName[assignment.Name] = assignment
	}

	normalized := strings.ReplaceAll(contents, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	var lines []string
	if normalized != "" {
		lines = strings.Split(normalized, "\n")
	}

	activeNames := map[string]bool{}
	for _, line := range lines {
		name, commented, ok := parseDotenvAssignmentLine(line)
		if ok && !commented {
			activeNames[name] = true
		}
	}

	written := map[string]bool{}
	for index, line := range lines {
		name, commented, ok := parseDotenvAssignmentLine(line)
		if !ok {
			continue
		}

		assignment, wanted := valueByName[name]
		if !wanted {
			continue
		}
		if commented && (activeNames[name] || written[name]) {
			continue
		}

		lines[index] = renderDotenvAssignment(assignment)
		written[name] = true
	}

	missing := make([]string, 0, len(assignments))
	for _, assignment := range assignments {
		if !written[assignment.Name] {
			missing = append(missing, renderDotenvAssignment(assignment))
		}
	}
	if len(missing) > 0 && len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
		lines = append(lines, "")
	}
	lines = append(lines, missing...)

	return strings.Join(lines, "\n") + "\n"
}

// parseDotenvAssignmentLine recognizes active, exported, and commented assignments.
func parseDotenvAssignmentLine(line string) (string, bool, bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	commented := false
	if strings.HasPrefix(trimmed, "#") {
		commented = true
		trimmed = strings.TrimLeftFunc(strings.TrimPrefix(trimmed, "#"), unicode.IsSpace)
	}
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimLeftFunc(strings.TrimPrefix(trimmed, "export "), unicode.IsSpace)
	}

	name, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", false, false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false, false
	}

	return name, commented, true
}

func renderDotenvAssignment(assignment dotenvAssignment) string {
	return assignment.Name + "=" + renderDotenvValue(assignment.Value)
}

// renderDotenvValue quotes and escapes characters with dotenv interpolation semantics.
func renderDotenvValue(value string) string {
	if value == "" || dotenvValueNeedsQuotes(value) {
		escaped := strings.ReplaceAll(value, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		escaped = strings.ReplaceAll(escaped, "\n", "\\n")
		escaped = strings.ReplaceAll(escaped, "\r", "\\r")
		escaped = strings.ReplaceAll(escaped, "\t", "\\t")
		escaped = strings.ReplaceAll(escaped, "$", "\\$")
		return "\"" + escaped + "\""
	}

	return value
}

// dotenvValueNeedsQuotes identifies values that cannot safely remain unquoted.
func dotenvValueNeedsQuotes(value string) bool {
	for _, char := range value {
		if unicode.IsLetter(char) || unicode.IsDigit(char) {
			continue
		}
		switch char {
		case '_', '-', '.', '/', ':', '@':
			continue
		default:
			return true
		}
	}

	return false
}

// phpSingleQuotedString escapes data using PHP single-quoted string rules.
func phpSingleQuotedString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")

	return "'" + escaped + "'"
}

// frameworkAppRootFromDocroot removes a declared public-directory suffix,
// including multi-directory layouts, from either absolute or relative docroots.
func frameworkAppRootFromDocroot(projectDir, docroot, publicDir string) string {
	projectDir = cleanAbsolutePath(projectDir)
	trimmed := strings.TrimSpace(docroot)
	if trimmed == "" {
		return projectDir
	}

	cleanDocroot := filepath.Clean(filepath.FromSlash(trimmed))
	appRoot := cleanDocroot
	if publicDir != "." && frameworkPathHasSuffix(cleanDocroot, publicDir) {
		for suffix := filepath.Clean(filepath.FromSlash(publicDir)); suffix != "."; suffix = filepath.Dir(suffix) {
			appRoot = filepath.Dir(appRoot)
		}
	}
	if filepath.IsAbs(appRoot) {
		return appRoot
	}
	return filepath.Join(projectDir, appRoot)
}

// frameworkPathHasSuffix compares complete path segments instead of string
// suffixes, so "notpublic" never matches "public".
func frameworkPathHasSuffix(filename, suffix string) bool {
	filename, suffix = filepath.Clean(filename), filepath.Clean(filepath.FromSlash(suffix))
	if suffix == "." {
		return filename == "."
	}
	for suffix != "." {
		if !strings.EqualFold(filepath.Base(filename), filepath.Base(suffix)) {
			return false
		}
		filename, suffix = filepath.Dir(filename), filepath.Dir(suffix)
	}
	return true
}

// frameworkDocrootPath honors explicit custom document roots while resolving a
// normal framework suffix against the selected Composer application root.
func frameworkDocrootPath(ctx PostComposerContext, appRoot, publicDir string) string {
	trimmed := strings.TrimSpace(ctx.Environment.Docroot)
	if trimmed == "" {
		return filepath.Join(appRoot, publicDir)
	}
	cleanDocroot := filepath.Clean(filepath.FromSlash(trimmed))
	if filepath.IsAbs(cleanDocroot) {
		return cleanDocroot
	}
	if frameworkPathHasSuffix(cleanDocroot, publicDir) {
		return filepath.Join(appRoot, publicDir)
	}

	return filepath.Join(cleanAbsolutePath(ctx.ProjectDir), cleanDocroot)
}

// composerCreateProjectDirectory resolves an explicit or package-derived target directory.
func composerCreateProjectDirectory(args []string, workingDir string) string {
	commandIndex := -1
	for index, arg := range args {
		if strings.EqualFold(strings.TrimSpace(arg), "create-project") {
			commandIndex = index
			break
		}
	}
	if commandIndex == -1 {
		return ""
	}

	positionals := make([]string, 0, 3)
	for _, arg := range args[commandIndex+1:] {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" || strings.HasPrefix(trimmed, "-") {
			continue
		}
		positionals = append(positionals, trimmed)
	}
	if len(positionals) == 0 {
		return ""
	}

	directory := ""
	if len(positionals) > 1 {
		directory = positionals[1]
	} else {
		directory = composerPackageDirectoryName(positionals[0])
	}
	if strings.TrimSpace(directory) == "" {
		return ""
	}
	if filepath.IsAbs(directory) {
		return filepath.Clean(directory)
	}

	base := cleanAbsolutePath(workingDir)
	if base == "" {
		base = "."
	}
	return filepath.Clean(filepath.Join(base, filepath.FromSlash(directory)))
}

// composerPackageDirectoryName removes vendor and version parts from a package name.
func composerPackageDirectoryName(name string) string {
	trimmed := strings.Trim(strings.TrimSpace(name), "/")
	if trimmed == "" {
		return ""
	}
	if slash := strings.LastIndex(trimmed, "/"); slash >= 0 {
		trimmed = trimmed[slash+1:]
	}
	if colon := strings.Index(trimmed, ":"); colon >= 0 {
		trimmed = trimmed[:colon]
	}

	return trimmed
}

// cleanAbsolutePath normalizes root candidates, retaining a clean relative fallback if resolution fails.
func cleanAbsolutePath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	absolute, err := filepath.Abs(trimmed)
	if err != nil {
		return filepath.Clean(trimmed)
	}

	return filepath.Clean(absolute)
}

// dedupePaths preserves candidate precedence with case-insensitive Windows identity.
func dedupePaths(paths []string) []string {
	seen := map[string]bool{}
	deduped := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := cleanAbsolutePath(path)
		if clean == "" {
			continue
		}
		key := clean
		if runtime.GOOS == "windows" {
			key = strings.ToLower(clean)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		deduped = append(deduped, clean)
	}

	return deduped
}
