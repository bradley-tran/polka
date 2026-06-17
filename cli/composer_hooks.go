package cli

import (
	"fmt"
	"strings"

	"polka/backend"
	"polka/plugins"
	"polka/service"
)

func runPostComposerHook(store backend.Store, tool string, args []string, workingDir string) error {
	if !strings.EqualFold(strings.TrimSpace(tool), "composer") || !isPostComposerCommand(args) {
		return nil
	}

	current, err := store.Current()
	if err != nil {
		return err
	}
	if current == nil || strings.TrimSpace(current.Framework) == "" {
		return nil
	}

	plugin, ok := store.FrameworkPlugin(current.Framework)
	if !ok {
		return fmt.Errorf("unsupported framework %q; supported frameworks: %s", current.Framework, strings.Join(store.SupportedFrameworks(), ", "))
	}

	credentials, err := ensurePostComposerDatabaseCredentials(store, *current)
	if err != nil {
		return err
	}

	return plugin.PostComposer(plugins.PostComposerContext{
		Environment: *current,
		ProjectDir:  store.ProjectDir,
		WorkingDir:  workingDir,
		Args:        append([]string(nil), args...),
		Database:    credentials,
	})
}

func isPostComposerCommand(args []string) bool {
	switch composerCommand(args) {
	case "install", "update", "create-project":
		return true
	default:
		return false
	}
}

func composerCommand(args []string) string {
	skipNext := false
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed == "" {
			continue
		}
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(trimmed, "-") {
			skipNext = composerOptionConsumesValue(trimmed)
			continue
		}

		return strings.ToLower(trimmed)
	}

	return ""
}

func composerOptionConsumesValue(option string) bool {
	if strings.Contains(option, "=") {
		return false
	}

	switch option {
	case "-d", "--working-dir":
		return true
	default:
		return false
	}
}

func ensurePostComposerDatabaseCredentials(store backend.Store, environment backend.Environment) (*plugins.DatabaseCredentials, error) {
	if environment.Database == nil || strings.TrimSpace(environment.Database.Engine) == "" {
		return nil, nil
	}

	credentials, err := service.EnsureManagedDatabaseCredentialAssets(store.RootDir, service.ResolvedDatabaseEnvironment{
		Environment: environment,
		Database:    environment.Database,
	})
	if err != nil {
		return nil, err
	}

	return frameworkDatabaseCredentialsFromManaged(environment, credentials), nil
}
