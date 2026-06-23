package cli

import (
	"reflect"
	"testing"

	"polka/backend"
)

func TestInjectPostgreSQLConnectionArgs(t *testing.T) {
	originalPing := pingDatabaseAddressFunc
	pingDatabaseAddressFunc = func(string) bool { return false }
	t.Cleanup(func() { pingDatabaseAddressFunc = originalPing })

	root := t.TempDir()
	resolved := dbResolvedEnvironment{
		Environment: backend.Environment{Name: "demo"},
		Database:    &backend.DatabaseConfig{Engine: "postgresql", Version: "17"},
	}
	args, err := injectDatabaseConnectionArgs(root, resolved, nil)
	if err != nil {
		t.Fatalf("injectDatabaseConnectionArgs() error = %v", err)
	}
	want := []string{
		"--host=127.0.0.1",
		"--port=5432",
		"--username=polka",
		"--dbname=demo",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("injectDatabaseConnectionArgs() = %#v, want %#v", args, want)
	}

	overrides := []string{"-hremote", "-p5544", "-Uadmin", "-dother"}
	args, err = injectDatabaseConnectionArgs(root, resolved, overrides)
	if err != nil {
		t.Fatalf("injectDatabaseConnectionArgs(overrides) error = %v", err)
	}
	if !reflect.DeepEqual(args, overrides) {
		t.Fatalf("injectDatabaseConnectionArgs(overrides) = %#v, want native args unchanged", args)
	}
}

func TestPostgreSQLConnectionOverrideDetection(t *testing.T) {
	host, port, user, database := postgreSQLConnectionOverrides([]string{"--host", "remote", "--port=5544", "-Uadmin", "--dbname", "demo"})
	if !host || !port || !user || !database {
		t.Fatalf("postgreSQLConnectionOverrides() = %v %v %v %v, want all overrides", host, port, user, database)
	}
}
