package plugins

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"unicode"
)

const (
	defaultCakePHPDocroot        = "webroot"
	polkaDrupalSettingsFile      = "settings.polka.php"
	polkaDrupalIncludeStart      = "// <polka:settings-polka>"
	polkaDrupalIncludeEnd        = "// </polka:settings-polka>"
	defaultDrupalDocroot         = "web"
	defaultWordPressDocroot      = "."
	defaultWordPressDatabaseHost = "127.0.0.1"
	defaultWordPressDatabasePort = 3306
	defaultWordPressTablePrefix  = "wp_"
)

type dotenvAssignment struct {
	Name  string
	Value string
}

func writeCakePHPConfigSecrets(ctx PostComposerContext) error {
	appRoot := frameworkComposerAppRoot(ctx, defaultCakePHPDocroot, CakePHP)
	return writeCakePHPConfigSecretsForAppRoot(ctx, appRoot)
}

func writeCakePHPConfigSecretsForAppRoot(ctx PostComposerContext, appRoot string) error {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	configPath := filepath.Join(appRoot, "config", "app_local.php")
	template, err := readCakePHPConfigTemplate(configPath, filepath.Join(appRoot, "config", "app_local.example.php"))
	if err != nil {
		return err
	}

	return writePHPSecretFile(configPath, renderCakePHPConfig(template, credentials))
}

func writeCodeIgniterDotenvSecrets(ctx PostComposerContext) error {
	values := codeIgniterDatabaseRuntimeEnv(RuntimeEnvContext{
		Environment: ctx.Environment,
		Database:    ctx.Database,
	})
	if len(values) == 0 {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, "public", CodeIgniter)
	return writeDotenvSecretFile(
		filepath.Join(appRoot, ".env"),
		filepath.Join(appRoot, "env"),
		orderedDotenvAssignments(values, []string{
			"database.default.hostname",
			"database.default.port",
			"database.default.database",
			"database.default.username",
			"database.default.password",
			"database.default.DBDriver",
		}),
	)
}

func writeDrupalSettingsSecrets(ctx PostComposerContext) error {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, defaultDrupalDocroot, Drupal)
	return writeDrupalSettingsSecretsForAppRoot(ctx, appRoot, defaultDrupalDocroot)
}

func writeDrupalSettingsSecretsForAppRoot(ctx PostComposerContext, appRoot, publicDir string) error {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	settingsDir := filepath.Join(frameworkDocrootPath(ctx, appRoot, publicDir), "sites", "default")
	settingsPath := filepath.Join(settingsDir, "settings.php")
	if err := ensureDrupalSettingsPHP(settingsPath, filepath.Join(settingsDir, "default.settings.php")); err != nil {
		return err
	}
	if err := ensureDrupalPolkaInclude(settingsPath); err != nil {
		return err
	}

	return writePHPSecretFile(filepath.Join(settingsDir, polkaDrupalSettingsFile), renderDrupalPolkaSettings(credentials))
}

func writeWordPressConfigSecrets(ctx PostComposerContext) error {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	appRoot := frameworkComposerAppRoot(ctx, defaultWordPressDocroot, WordPress)
	return writeWordPressConfigSecretsForAppRoot(ctx, appRoot)
}

func writeWordPressConfigSecretsForAppRoot(ctx PostComposerContext, appRoot string) error {
	credentials := normalizeDatabaseCredentials(ctx.Database)
	if credentials == nil {
		return nil
	}

	configPath := filepath.Join(appRoot, "wp-config.php")
	template, err := readWordPressConfigTemplate(configPath, filepath.Join(appRoot, "wp-config-sample.php"))
	if err != nil {
		return err
	}

	return writePHPSecretFile(configPath, renderWordPressConfig(template, credentials))
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

func normalizeDatabaseCredentials(credentials *DatabaseCredentials) *DatabaseCredentials {
	if credentials == nil {
		return nil
	}

	normalized := &DatabaseCredentials{
		Host:         strings.TrimSpace(credentials.Host),
		Port:         credentials.Port,
		DatabaseName: strings.TrimSpace(credentials.DatabaseName),
		User:         strings.TrimSpace(credentials.User),
		Password:     credentials.Password,
	}
	if normalized.Host == "" {
		normalized.Host = defaultWordPressDatabaseHost
	}
	if normalized.Port == 0 {
		normalized.Port = defaultWordPressDatabasePort
	}
	if normalized.DatabaseName == "" || normalized.User == "" {
		return nil
	}

	return normalized
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

func writePHPSecretFile(path, contents string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(contents), 0o600)
}

func readCakePHPConfigTemplate(path, fallbackPath string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(fallbackPath)
		if errors.Is(err, os.ErrNotExist) {
			return minimalCakePHPConfig(), nil
		}
	}
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func renderCakePHPConfig(contents string, credentials *DatabaseCredentials) string {
	assignments := map[string]string{
		"driver":   "Cake\\Database\\Driver\\Mysql",
		"host":     credentials.Host,
		"port":     strconv.Itoa(credentials.Port),
		"username": credentials.User,
		"password": credentials.Password,
		"database": credentials.DatabaseName,
		"encoding": "utf8mb4",
	}
	order := []string{"driver", "host", "port", "username", "password", "database", "encoding"}

	normalized := strings.ReplaceAll(contents, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	if strings.TrimSpace(normalized) == "" {
		return minimalCakePHPConfigForCredentials(credentials)
	}

	lines := strings.Split(normalized, "\n")
	path := []string{}
	written := map[string]bool{}
	defaultDatasourceSeen := false
	updated := make([]string, 0, len(lines)+len(order))

	for _, line := range lines {
		if cakePHPConfigPathIsDefaultDatasource(path) && isPHPArrayCloseLine(line) {
			defaultDatasourceSeen = true
			for _, key := range order {
				if !written[key] {
					updated = append(updated, cakePHPConfigAssignmentLine(cakePHPClosingIndent(line), key, assignments[key]))
					written[key] = true
				}
			}
		}

		if cakePHPConfigPathIsDefaultDatasource(path) {
			if key, ok := parsePHPScalarArrayKey(line); ok {
				if value, wanted := assignments[key]; wanted {
					line = cakePHPConfigAssignmentLine(cakePHPAssignmentIndent(line), key, value)
					written[key] = true
				}
			}
		}
		updated = append(updated, line)

		if key, ok := parsePHPArrayOpenKey(line); ok {
			path = append(path, key)
		}
		for closes := countPHPArrayCloses(line); closes > 0 && len(path) > 0; closes-- {
			path = path[:len(path)-1]
		}
	}
	if !defaultDatasourceSeen {
		return minimalCakePHPConfigForCredentials(credentials)
	}

	return strings.Join(updated, "\n") + "\n"
}

func minimalCakePHPConfig() string {
	return minimalCakePHPConfigForCredentials(&DatabaseCredentials{
		Host:         defaultDatabaseHost,
		Port:         defaultDatabasePort,
		DatabaseName: "app",
		User:         "root",
	})
}

func minimalCakePHPConfigForCredentials(credentials *DatabaseCredentials) string {
	return strings.Join([]string{
		"<?php",
		"declare(strict_types=1);",
		"",
		"return [",
		"    'Datasources' => [",
		"        'default' => [",
		cakePHPConfigAssignmentLine("            ", "driver", "Cake\\Database\\Driver\\Mysql"),
		cakePHPConfigAssignmentLine("            ", "host", credentials.Host),
		cakePHPConfigAssignmentLine("            ", "port", strconv.Itoa(credentials.Port)),
		cakePHPConfigAssignmentLine("            ", "username", credentials.User),
		cakePHPConfigAssignmentLine("            ", "password", credentials.Password),
		cakePHPConfigAssignmentLine("            ", "database", credentials.DatabaseName),
		cakePHPConfigAssignmentLine("            ", "encoding", "utf8mb4"),
		"        ],",
		"    ],",
		"];",
		"",
	}, "\n")
}

func cakePHPConfigAssignmentLine(indent, key, value string) string {
	return indent + phpSingleQuotedString(key) + " => " + phpSingleQuotedString(value) + ","
}

func cakePHPConfigPathIsDefaultDatasource(path []string) bool {
	return len(path) == 2 && path[0] == "Datasources" && path[1] == "default"
}

func parsePHPArrayOpenKey(line string) (string, bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, rest, ok := parsePHPArrayKeyPrefix(trimmed)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "[") {
		return key, true
	}

	return "", false
}

func parsePHPScalarArrayKey(line string) (string, bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") {
		return "", false
	}
	key, rest, ok := parsePHPArrayKeyPrefix(trimmed)
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "[") {
		return "", false
	}

	return key, true
}

func parsePHPArrayKeyPrefix(trimmed string) (string, string, bool) {
	if len(trimmed) < 4 || trimmed[0] != '\'' && trimmed[0] != '"' {
		return "", "", false
	}
	quote := trimmed[0]
	end := 1
	escaped := false
	for ; end < len(trimmed); end++ {
		char := trimmed[end]
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == quote {
			break
		}
	}
	if end >= len(trimmed) {
		return "", "", false
	}
	remainder := strings.TrimLeftFunc(trimmed[end+1:], unicode.IsSpace)
	if !strings.HasPrefix(remainder, "=>") {
		return "", "", false
	}

	return trimmed[1:end], strings.TrimLeftFunc(strings.TrimPrefix(remainder, "=>"), unicode.IsSpace), true
}

func isPHPArrayCloseLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	return strings.HasPrefix(trimmed, "]")
}

func cakePHPClosingIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))] + "    "
}

func cakePHPAssignmentIndent(line string) string {
	return line[:len(line)-len(strings.TrimLeftFunc(line, unicode.IsSpace))]
}

func countPHPArrayCloses(line string) int {
	count := 0
	quote := rune(0)
	escaped := false
	for _, char := range line {
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if char == '\\' {
				escaped = true
				continue
			}
			if char == quote {
				quote = 0
			}
			continue
		}
		if char == '\'' || char == '"' {
			quote = char
			continue
		}
		if char == ']' {
			count++
		}
	}

	return count
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

func ensureDrupalSettingsPHP(path, fallbackPath string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	data, err := os.ReadFile(fallbackPath)
	if errors.Is(err, os.ErrNotExist) {
		data = []byte("<?php\n")
		err = nil
	}
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	return os.WriteFile(path, data, 0o644)
}

func ensureDrupalPolkaInclude(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	contents := string(data)
	if strings.Contains(contents, polkaDrupalIncludeStart) {
		return nil
	}
	contents = strings.TrimRight(contents, "\r\n")
	if strings.TrimSpace(contents) != "" {
		contents += "\n\n"
	}
	contents += strings.Join([]string{
		polkaDrupalIncludeStart,
		"if (file_exists($app_root . '/' . $site_path . '/" + polkaDrupalSettingsFile + "')) {",
		"  include $app_root . '/' . $site_path . '/" + polkaDrupalSettingsFile + "';",
		"}",
		polkaDrupalIncludeEnd,
		"",
	}, "\n")

	return os.WriteFile(path, []byte(contents), 0o644)
}

func renderDrupalPolkaSettings(credentials *DatabaseCredentials) string {
	return strings.Join([]string{
		"<?php",
		"",
		"// Generated by Polka. Re-run composer install, update, or create-project through Polka after editing database settings.",
		"$databases['default']['default'] = [",
		"  'database' => " + phpSingleQuotedString(credentials.DatabaseName) + ",",
		"  'username' => " + phpSingleQuotedString(credentials.User) + ",",
		"  'password' => " + phpSingleQuotedString(credentials.Password) + ",",
		"  'prefix' => '',",
		"  'host' => " + phpSingleQuotedString(credentials.Host) + ",",
		"  'port' => " + phpSingleQuotedString(strconv.Itoa(credentials.Port)) + ",",
		"  'namespace' => 'Drupal\\\\mysql\\\\Driver\\\\Database\\\\mysql',",
		"  'driver' => 'mysql',",
		"  'autoload' => 'core/modules/mysql/src/Driver/Database/mysql/',",
		"];",
		"",
	}, "\n")
}

func readWordPressConfigTemplate(path, fallbackPath string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		data, err = os.ReadFile(fallbackPath)
		if errors.Is(err, os.ErrNotExist) {
			return minimalWordPressConfig(), nil
		}
	}
	if err != nil {
		return "", err
	}

	return string(data), nil
}

func renderWordPressConfig(contents string, credentials *DatabaseCredentials) string {
	assignments := map[string]string{
		"DB_NAME":     credentials.DatabaseName,
		"DB_USER":     credentials.User,
		"DB_PASSWORD": credentials.Password,
		"DB_HOST":     wordPressDatabaseHost(credentials),
	}

	normalized := strings.ReplaceAll(contents, "\r\n", "\n")
	normalized = strings.TrimSuffix(normalized, "\n")
	var lines []string
	if normalized != "" {
		lines = strings.Split(normalized, "\n")
	}

	written := map[string]bool{}
	for index, line := range lines {
		for key, value := range assignments {
			updated, ok := replaceWordPressDefine(line, key, value)
			if !ok {
				continue
			}
			lines[index] = updated
			written[key] = true
			break
		}
	}

	missing := []string{}
	for _, key := range []string{"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_HOST"} {
		if !written[key] {
			missing = append(missing, wordPressDefineLine(key, assignments[key]))
		}
	}
	if len(missing) > 0 {
		lines = insertWordPressDefines(lines, missing)
	}

	return strings.Join(lines, "\n") + "\n"
}

func replaceWordPressDefine(line, key, value string) (string, bool) {
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "#") || !isPHPDefineCall(trimmed) {
		return "", false
	}
	if !strings.Contains(trimmed, phpSingleQuotedString(key)) && !strings.Contains(trimmed, phpDoubleQuotedString(key)) {
		return "", false
	}

	indent := line[:len(line)-len(trimmed)]
	return indent + wordPressDefineLine(key, value), true
}

func isPHPDefineCall(trimmed string) bool {
	if !strings.HasPrefix(trimmed, "define") {
		return false
	}

	remainder := strings.TrimLeftFunc(strings.TrimPrefix(trimmed, "define"), unicode.IsSpace)
	return strings.HasPrefix(remainder, "(")
}

func insertWordPressDefines(lines []string, defines []string) []string {
	insertAt := len(lines)
	for index, line := range lines {
		if strings.Contains(line, "DB_CHARSET") {
			insertAt = index
			break
		}
	}

	inserted := make([]string, 0, len(lines)+len(defines)+2)
	inserted = append(inserted, lines[:insertAt]...)
	if len(inserted) > 0 && strings.TrimSpace(inserted[len(inserted)-1]) != "" {
		inserted = append(inserted, "")
	}
	inserted = append(inserted, defines...)
	if insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) != "" {
		inserted = append(inserted, "")
	}
	inserted = append(inserted, lines[insertAt:]...)

	return inserted
}

func wordPressDefineLine(key, value string) string {
	return "define( " + phpSingleQuotedString(key) + ", " + phpSingleQuotedString(value) + " );"
}

func wordPressDatabaseHost(credentials *DatabaseCredentials) string {
	if credentials.Port == 0 || credentials.Port == defaultWordPressDatabasePort {
		return credentials.Host
	}

	return credentials.Host + ":" + strconv.Itoa(credentials.Port)
}

func minimalWordPressConfig() string {
	return strings.Join([]string{
		"<?php",
		wordPressDefineLine("DB_NAME", "database_name_here"),
		wordPressDefineLine("DB_USER", "username_here"),
		wordPressDefineLine("DB_PASSWORD", "password_here"),
		wordPressDefineLine("DB_HOST", "localhost"),
		wordPressDefineLine("DB_CHARSET", "utf8mb4"),
		wordPressDefineLine("DB_COLLATE", ""),
		"",
		"$table_prefix = " + phpSingleQuotedString(defaultWordPressTablePrefix) + ";",
		"",
		"if ( ! defined( 'ABSPATH' ) ) {",
		"  define( 'ABSPATH', __DIR__ . '/' );",
		"}",
		"",
		"require_once ABSPATH . 'wp-settings.php';",
		"",
	}, "\n")
}

func phpSingleQuotedString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "'", "\\'")

	return "'" + escaped + "'"
}

func phpDoubleQuotedString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")

	return "\"" + escaped + "\""
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

func drupalDocrootPath(ctx PostComposerContext, appRoot string) string {
	return frameworkDocrootPath(ctx, appRoot, defaultDrupalDocroot)
}

func frameworkDocrootPath(ctx PostComposerContext, appRoot, publicDir string) string {
	trimmed := strings.TrimSpace(ctx.Environment.Docroot)
	if trimmed == "" {
		return filepath.Join(appRoot, publicDir)
	}
	cleanDocroot := filepath.Clean(filepath.FromSlash(trimmed))
	if filepath.IsAbs(cleanDocroot) {
		return cleanDocroot
	}
	if strings.EqualFold(filepath.Base(cleanDocroot), publicDir) {
		return filepath.Join(appRoot, publicDir)
	}

	return filepath.Join(cleanAbsolutePath(ctx.ProjectDir), cleanDocroot)
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
	case CakePHP:
		return regularFileExists(filepath.Join(path, "bin", "cake")) ||
			regularFileExists(filepath.Join(path, "config", "app.php")) ||
			regularFileExists(filepath.Join(path, "config", "app_local.php")) ||
			regularFileExists(filepath.Join(path, "composer.json"))
	case CodeIgniter:
		return regularFileExists(filepath.Join(path, "spark")) ||
			regularFileExists(filepath.Join(path, "env")) ||
			regularFileExists(filepath.Join(path, ".env")) ||
			regularFileExists(filepath.Join(path, "composer.json"))
	case Drupal:
		return regularFileExists(filepath.Join(path, "composer.json")) ||
			regularFileExists(filepath.Join(path, defaultDrupalDocroot, "sites", "default", "default.settings.php")) ||
			regularFileExists(filepath.Join(path, "sites", "default", "default.settings.php"))
	case WordPress:
		return regularFileExists(filepath.Join(path, "wp-config.php")) ||
			regularFileExists(filepath.Join(path, "wp-config-sample.php")) ||
			regularFileExists(filepath.Join(path, "wp-load.php")) ||
			regularFileExists(filepath.Join(path, "composer.json"))
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
