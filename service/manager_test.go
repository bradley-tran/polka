package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polka/config"
	"polka/tools"
)

func TestManagerStartSkipsConfiguredMailpitWithoutMatchingTool(t *testing.T) {
	ctx, warnings := managerTestContext(t, config.Environment{
		Name:    "demo",
		Mailpit: &config.MailpitConfig{Version: "1.30"},
	})

	started := false
	_, err := DefaultManager().Start(ctx, RuntimeHooks{
		Mailpit: MailpitRuntimeHooks{
			StartServer: func(MailpitServerSpec) (MailpitStartResult, error) {
				started = true
				return MailpitStartResult{}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Start(mailpit missing tool) error = %v", err)
	}
	if started {
		t.Fatal("Start(mailpit missing tool) started mailpit, want skipped")
	}
	if !strings.Contains(warnings.String(), `matching tool "mailpit" is not registered`) {
		t.Fatalf("warnings = %q, want missing mailpit warning", warnings.String())
	}
}

func TestManagerStartSkipsConfiguredPHPMyAdminWithoutMatchingTool(t *testing.T) {
	ctx, warnings := managerTestContext(t, config.Environment{
		Name:       "demo",
		PHPMyAdmin: &config.PHPMyAdminConfig{Version: "5.2"},
	})

	started := false
	result, err := DefaultManager().Start(ctx, RuntimeHooks{
		PHPMyAdmin: PHPMyAdminRuntimeHooks{
			StartServe: func(config.Environment, Endpoint, AppLayout, string) (ServeRuntimeState, error) {
				started = true
				return ServeRuntimeState{}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Start(phpmyadmin missing tool) error = %v", err)
	}
	if result.PHPMyAdmin != nil {
		t.Fatalf("Start(phpmyadmin missing tool) result = %#v, want nil phpmyadmin result", result.PHPMyAdmin)
	}
	if started {
		t.Fatal("Start(phpmyadmin missing tool) started phpmyadmin, want skipped")
	}
	if !strings.Contains(warnings.String(), `matching tool "phpmyadmin" is not registered`) {
		t.Fatalf("warnings = %q, want missing phpmyadmin warning", warnings.String())
	}
}

func TestManagerStartSkipsConfiguredDatabaseWithoutMatchingTool(t *testing.T) {
	ctx, warnings := managerTestContext(t, config.Environment{
		Name:     "demo",
		Database: &config.DatabaseConfig{Engine: "mysql", Version: "8.4"},
	})

	started := false
	_, err := DefaultManager().Start(ctx, RuntimeHooks{
		Database: DatabaseRuntimeHooks{
			StartServer: func(ManagedDatabaseServerSpec) (ManagedDatabaseStartResult, error) {
				started = true
				return ManagedDatabaseStartResult{}, nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Start(database missing tool) error = %v", err)
	}
	if started {
		t.Fatal("Start(database missing tool) started database, want skipped")
	}
	if !strings.Contains(warnings.String(), `matching tool "mysql" is not registered`) {
		t.Fatalf("warnings = %q, want missing mysql warning", warnings.String())
	}
}

func TestManagerStopCleansExistingStateWithoutMatchingTool(t *testing.T) {
	ctx, _ := managerTestContext(t, config.Environment{Name: "demo"})
	state := MailpitRuntimeState{
		EnvironmentName: "demo",
		Version:         "1.30",
		SMTPPort:        1125,
		UIPort:          8125,
		PID:             os.Getpid(),
		StartedAt:       time.Now().UTC(),
	}
	if err := WriteMailpitState(MailpitStatePath(ctx.RootDir, "demo"), state); err != nil {
		t.Fatalf("WriteMailpitState() error = %v", err)
	}

	stopped := false
	result, err := DefaultManager().Stop(ctx, RuntimeHooks{
		Mailpit: MailpitRuntimeHooks{
			PingAddress: func(address string) bool {
				return address == MailpitAddress(1125) || address == MailpitAddress(8125)
			},
			StopServer: func(state MailpitRuntimeState) error {
				stopped = true
				return nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Stop(existing mailpit without tool) error = %v", err)
	}
	if !stopped {
		t.Fatal("Stop(existing mailpit without tool) did not call StopServer")
	}
	if result.Mailpit == nil || result.Mailpit.AlreadyStopped {
		t.Fatalf("Stop(existing mailpit without tool) mailpit result = %#v, want stopped state", result.Mailpit)
	}
	if _, err := os.Stat(MailpitStatePath(ctx.RootDir, "demo")); !os.IsNotExist(err) {
		t.Fatalf("mailpit state after stop error = %v, want not exists", err)
	}
}

func managerTestContext(t *testing.T, environment config.Environment) (Context, *strings.Builder) {
	t.Helper()

	registry, err := tools.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	rootDir := filepath.Join(t.TempDir(), ".polka")
	warnings := &strings.Builder{}
	ctx := Context{
		RootDir:     rootDir,
		EnvsDir:     filepath.Join(rootDir, "envs"),
		Environment: environment,
		Registry:    registry,
		Warnf: func(format string, args ...any) {
			warnings.WriteString(formatWarning(format, args...))
		},
	}

	return ctx, warnings
}

func formatWarning(format string, args ...any) string {
	return strings.TrimSpace(fmt.Sprintf(format, args...))
}
