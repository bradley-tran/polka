package backend

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"polka/config"
)

const (
	PHPMyAdminListenHost                 = "127.0.0.1"
	DefaultPHPMyAdminPort                = 8081
	phpMyAdminStorageCreateTablesSQLPath = "sql/create_tables.sql"
)

func normalizePHPMyAdminConfig(phpMyAdmin *PHPMyAdminConfig) *PHPMyAdminConfig {
	return config.NormalizePHPMyAdminConfig(phpMyAdmin)
}

func EffectivePHPMyAdminPort(phpMyAdmin *PHPMyAdminConfig) int {
	if phpMyAdmin == nil || phpMyAdmin.Port == 0 {
		return DefaultPHPMyAdminPort
	}

	return phpMyAdmin.Port
}

func PHPMyAdminAddress(port int) string {
	return net.JoinHostPort(PHPMyAdminListenHost, strconv.Itoa(port))
}

func ResolvePHPMyAdminDocroot(envsDir, version string) (string, error) {
	trimmedVersion := strings.TrimSpace(version)
	if trimmedVersion == "" {
		return "", fmt.Errorf("phpmyadmin version cannot be empty")
	}

	docroot := filepath.Join(envsDir, toolPHPMyAdmin, trimmedVersion)
	indexPath := filepath.Join(docroot, "index.php")
	fileInfo, err := os.Stat(indexPath)
	if err != nil {
		return "", fmt.Errorf("phpmyadmin version %q is not installed under %s", trimmedVersion, docroot)
	}
	if fileInfo.IsDir() {
		return "", fmt.Errorf("phpmyadmin version %q has a directory where index.php was expected under %s", trimmedVersion, docroot)
	}

	return docroot, nil
}

func EnsurePHPMyAdminStorageConfigured(store Store, environment Environment, hooks DatabaseRuntimeHooks) error {
	if environment.PHPMyAdmin == nil || strings.TrimSpace(environment.PHPMyAdmin.Version) == "" {
		return nil
	}
	if environment.Database == nil || strings.TrimSpace(environment.Database.Engine) == "" {
		return nil
	}

	docroot, err := ResolvePHPMyAdminDocroot(store.EnvsDir, environment.PHPMyAdmin.Version)
	if err != nil {
		return err
	}
	createTablesPath := filepath.Join(docroot, filepath.FromSlash(phpMyAdminStorageCreateTablesSQLPath))
	fileInfo, err := os.Stat(createTablesPath)
	if err != nil {
		return fmt.Errorf("stat phpmyadmin storage SQL %s: %w", createTablesPath, err)
	}
	if fileInfo.IsDir() {
		return fmt.Errorf("phpmyadmin storage SQL %s is a directory", createTablesPath)
	}

	resolved := ResolvedDatabaseEnvironment{Environment: environment, Database: environment.Database}
	state, _, err := EnsureManagedDatabaseStarted(store, resolved, hooks)
	if err != nil {
		return fmt.Errorf("start database for phpmyadmin storage: %w", err)
	}

	if err := importPHPMyAdminStorageSQL(store, resolved, state, createTablesPath); err != nil {
		return err
	}

	return nil
}

func importPHPMyAdminStorageSQL(store Store, resolved ResolvedDatabaseEnvironment, state ManagedDatabaseRuntimeState, sqlPath string) error {
	target, err := store.resolveInstalledTool(resolved.Database.Engine, resolved.Database.Version)
	if err != nil {
		return err
	}
	if _, err := EnsureManagedDatabaseCredentialAssets(store.RootDir, resolved); err != nil {
		return err
	}

	sqlFile, err := os.Open(sqlPath)
	if err != nil {
		return fmt.Errorf("open phpmyadmin storage SQL %s: %w", sqlPath, err)
	}
	defer sqlFile.Close()

	port := state.Port
	if port == 0 {
		port = EffectiveDatabasePort(resolved.Database)
	}
	args := []string{
		"--defaults-extra-file=" + DatabaseDefaultsFilePath(store.RootDir, resolved.Environment.Name),
		"--protocol=tcp",
		"--host=" + DatabaseListenHost,
		"--port=" + strconv.Itoa(port),
	}
	output := &bytes.Buffer{}
	if err := executeDatabaseSQL(target, args, sqlFile, output); err != nil {
		detail := strings.TrimSpace(output.String())
		if detail == "" {
			return fmt.Errorf("import phpmyadmin storage SQL: %w", err)
		}

		return fmt.Errorf("import phpmyadmin storage SQL: %w (%s)", err, detail)
	}

	return nil
}

func executeDatabaseSQL(target string, args []string, input io.Reader, output io.Writer) error {
	command, err := prepareDatabaseCommand(target, args)
	if err != nil {
		return err
	}
	command.Stdin = input
	command.Stdout = output
	command.Stderr = output

	return command.Run()
}
