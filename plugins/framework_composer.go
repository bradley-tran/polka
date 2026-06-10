package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
)

type dotenvAssignment struct {
	Name  string
	Value string
}

func frameworkPostComposer(ctx PostComposerContext, id string) error {
	switch strings.ToLower(strings.TrimSpace(id)) {
	case Laravel:
		return writeLaravelDotenvSecrets(ctx)
	case Symfony:
		return writeSymfonyDotenvSecrets(ctx)
	default:
		return nil
	}
}

func writeLaravelDotenvSecrets(ctx PostComposerContext) error {
	values := frameworkDatabaseRuntimeEnv(RuntimeEnvContext{
		Environment: ctx.Environment,
		Database:    ctx.Database,
	}, true, false)
	if len(values) == 0 {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, "public", Laravel)
	return writeDotenvSecretFile(
		filepath.Join(appRoot, ".env"),
		filepath.Join(appRoot, ".env.example"),
		orderedDotenvAssignments(values, []string{
			"DB_CONNECTION",
			"DB_HOST",
			"DB_PORT",
			"DB_DATABASE",
			"DB_USERNAME",
			"DB_PASSWORD",
		}),
	)
}

func writeSymfonyDotenvSecrets(ctx PostComposerContext) error {
	values := frameworkDatabaseRuntimeEnv(RuntimeEnvContext{
		Environment: ctx.Environment,
		Database:    ctx.Database,
	}, false, true)
	if strings.TrimSpace(values["DATABASE_URL"]) == "" {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, "public", Symfony)
	return writeDotenvSecretFile(
		filepath.Join(appRoot, ".env.local"),
		"",
		orderedDotenvAssignments(values, []string{"DATABASE_URL"}),
	)
}

func orderedDotenvAssignments(values map[string]string, order []string) []dotenvAssignment {
	assignments := make([]dotenvAssignment, 0, len(order))
	for _, key := range order {
		value, ok := values[key]
		if ok {
			assignments = append(assignments, dotenvAssignment{Name: key, Value: value})
		}
	}

	return assignments
}

// writeDotenvSecretFile creates or updates a dotenv-style secret file without discarding unrelated keys.
func writeDotenvSecretFile(path, fallbackPath string, assignments []dotenvAssignment) error {
	if len(assignments) == 0 {
		return nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if strings.TrimSpace(fallbackPath) == "" {
			data = nil
			err = nil
		} else {
			data, err = os.ReadFile(fallbackPath)
			if errors.Is(err, os.ErrNotExist) {
				data = nil
				err = nil
			}
		}
	}
	if err != nil {
		return err
	}

	updated := upsertDotenvAssignments(string(data), assignments)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(updated), 0o600)
}

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

func renderDotenvValue(value string) string {
	if value == "" || dotenvValueNeedsQuotes(value) {
		escaped := strings.ReplaceAll(value, "\\", "\\\\")
		escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
		escaped = strings.ReplaceAll(escaped, "\n", "\\n")
		return "\"" + escaped + "\""
	}

	return value
}

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

func frameworkComposerAppRoot(ctx PostComposerContext, publicDir, framework string) string {
	candidates := make([]string, 0, 4)
	if target := composerCreateProjectDirectory(ctx.Args, ctx.WorkingDir); target != "" {
		candidates = append(candidates, target)
	}
	if docroot := frameworkAppRootFromDocroot(ctx.ProjectDir, ctx.Environment.Docroot, publicDir); docroot != "" {
		candidates = append(candidates, docroot)
	}
	if workingDir := cleanAbsolutePath(ctx.WorkingDir); workingDir != "" {
		candidates = append(candidates, workingDir)
	}
	if projectDir := cleanAbsolutePath(ctx.ProjectDir); projectDir != "" {
		candidates = append(candidates, projectDir)
	}

	for _, candidate := range dedupePaths(candidates) {
		if frameworkAppRootLooksLike(candidate, framework) {
			return candidate
		}
	}
	if len(candidates) > 0 {
		return candidates[0]
	}

	return "."
}

func frameworkAppRootFromDocroot(projectDir, docroot, publicDir string) string {
	projectDir = cleanAbsolutePath(projectDir)
	trimmed := strings.TrimSpace(docroot)
	if trimmed == "" {
		return projectDir
	}

	cleanDocroot := filepath.Clean(filepath.FromSlash(trimmed))
	if filepath.IsAbs(cleanDocroot) {
		if strings.EqualFold(filepath.Base(cleanDocroot), publicDir) {
			return filepath.Dir(cleanDocroot)
		}
		return cleanDocroot
	}
	if strings.EqualFold(filepath.Base(cleanDocroot), publicDir) {
		appRoot := filepath.Dir(cleanDocroot)
		if appRoot == "." {
			return projectDir
		}
		return filepath.Join(projectDir, appRoot)
	}

	return filepath.Join(projectDir, cleanDocroot)
}

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

func frameworkAppRootLooksLike(path, framework string) bool {
	switch strings.ToLower(strings.TrimSpace(framework)) {
	case Laravel:
		return regularFileExists(filepath.Join(path, "artisan")) ||
			regularFileExists(filepath.Join(path, ".env")) ||
			regularFileExists(filepath.Join(path, ".env.example")) ||
			regularFileExists(filepath.Join(path, "composer.json"))
	case Symfony:
		return regularFileExists(filepath.Join(path, "bin", "console")) ||
			regularFileExists(filepath.Join(path, ".env")) ||
			regularFileExists(filepath.Join(path, ".env.local")) ||
			regularFileExists(filepath.Join(path, "composer.json"))
	default:
		return false
	}
}

func regularFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

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
