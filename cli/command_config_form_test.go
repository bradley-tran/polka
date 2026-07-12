package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"polka/backend"
	"polka/config"
)

// testConfigFormEnvironment returns a fully-populated environment for form
// field construction tests, including dynamic map entries and PIE/PECL
// extensions that must be excluded from the form.
func testConfigFormEnvironment() backend.Environment {
	return backend.Environment{
		Name:          "demo",
		Framework:     "laravel",
		PHPVersion:    "8.4",
		HTTPS:         true,
		MemoryLimit:   "256M",
		OPcachePreset: "dev",
		EnvVars:       map[string]string{"B_VAR": "2", "A_VAR": "1"},
		PHPExtensions: map[string]bool{"intl": true},
		PIEExtensions: map[string]string{"vendor/example": "^1.0"},
		PECLExtensions: map[string]config.PECLExtensionConfig{
			"imagick": {Version: "3.7.0"},
		},
		OPcacheConfig: map[string]string{"opcache.jit": "tracing"},
		Server:        &config.ServerConfig{Type: "NGINX", Hostname: "demo.localhost", Port: 8080},
		Database:      &config.DatabaseConfig{Engine: "mysql", Port: 3306},
		Redis:         &config.RedisConfig{Version: "7.4", Port: 6379, Password: "secret"},
	}
}

// configFormFieldByKey returns the field with the given ConfigureValue key,
// failing the test when it is absent.
func configFormFieldByKey(t *testing.T, fields []*configFormField, key string) *configFormField {
	t.Helper()

	for _, field := range fields {
		if field.key == key {
			return field
		}
	}

	t.Fatalf("buildConfigFormFields() missing field %q", key)
	return nil
}

// configFormFieldIndex returns the position of a field key, or -1 when absent.
func configFormFieldIndex(fields []*configFormField, key string) int {
	for i, field := range fields {
		if field.key == key {
			return i
		}
	}

	return -1
}

func TestBuildConfigFormFieldsPrefillsCurrentValues(t *testing.T) {
	fields := buildConfigFormFields(testConfigFormEnvironment())

	tests := []struct {
		key     string
		kind    configFieldKind
		initial string
	}{
		{key: "framework", kind: configFieldInput, initial: "laravel"},
		{key: "tools.php", kind: configFieldInput, initial: "8.4"},
		{key: "https", kind: configFieldConfirm, initial: "true"},
		{key: "memory-limit", kind: configFieldInput, initial: "256M"},
		{key: "opcache-preset", kind: configFieldSelect, initial: "dev"},
		{key: "server.type", kind: configFieldSelect, initial: "nginx"},
		{key: "server.hostname", kind: configFieldInput, initial: "demo.localhost"},
		{key: "server.port", kind: configFieldPort, initial: "8080"},
		{key: "database.engine", kind: configFieldSelect, initial: "mysql"},
		{key: "database.port", kind: configFieldPort, initial: "3306"},
		{key: "tools.redis", kind: configFieldInput, initial: "7.4"},
		{key: "settings.redis.port", kind: configFieldPort, initial: "6379"},
		{key: "settings.redis.password", kind: configFieldInput, initial: "secret"},
		{key: "env-vars.A_VAR", kind: configFieldInput, initial: "1"},
		{key: "php-extensions.intl", kind: configFieldConfirm, initial: "true"},
		{key: "opcache-config.opcache.jit", kind: configFieldInput, initial: "tracing"},
	}
	for _, test := range tests {
		field := configFormFieldByKey(t, fields, test.key)
		if field.kind != test.kind {
			t.Errorf("field %q kind = %d, want %d", test.key, field.kind, test.kind)
		}
		if field.initial != test.initial {
			t.Errorf("field %q initial = %q, want %q", test.key, field.initial, test.initial)
		}
		if field.value != field.initial {
			t.Errorf("field %q value = %q, want the initial value %q", test.key, field.value, field.initial)
		}
	}

	// An unset port renders as an empty editable field, not "0".
	if field := configFormFieldByKey(t, fields, "settings.mailpit.smtp-port"); field.initial != "" {
		t.Errorf("unset port initial = %q, want empty", field.initial)
	}

	// Dynamic map fields appear in sorted key order.
	if a, b := configFormFieldIndex(fields, "env-vars.A_VAR"), configFormFieldIndex(fields, "env-vars.B_VAR"); a == -1 || b == -1 || a > b {
		t.Errorf("env var field order = A_VAR at %d, B_VAR at %d, want sorted", a, b)
	}

	// PIE and PECL extensions are owned by 'polka ext' and must not appear.
	for _, key := range []string{"php-extensions.vendor/example", "pecl-extensions.imagick", "tools.pie"} {
		if configFormFieldIndex(fields, key) != -1 {
			t.Errorf("buildConfigFormFields() unexpectedly includes field %q", key)
		}
	}
}

func TestBuildConfigFormFieldsSelectOptionsIncludeUnset(t *testing.T) {
	fields := buildConfigFormFields(backend.Environment{})

	tests := []struct {
		key     string
		options []string
	}{
		{key: "opcache-preset", options: []string{"", "none", "dev", "production"}},
		{key: "server.type", options: []string{"", "php", "nginx", "apache", "frankenphp"}},
		{key: "database.engine", options: []string{"", "mysql", "mariadb", "postgresql"}},
	}
	for _, test := range tests {
		field := configFormFieldByKey(t, fields, test.key)
		if len(field.options) != len(test.options) {
			t.Fatalf("field %q options = %v, want %v", test.key, field.options, test.options)
		}
		for i, option := range test.options {
			if field.options[i] != option {
				t.Errorf("field %q options[%d] = %q, want %q", test.key, i, field.options[i], option)
			}
		}
	}
}

// TestBuildConfigFormFieldsRoundTrip verifies every form field key is accepted
// by the backend write path, so the form cannot drift from the ConfigureValue
// catalog.
func TestBuildConfigFormFieldsRoundTrip(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	for _, field := range buildConfigFormFields(testConfigFormEnvironment()) {
		// Choose a value that is valid for every key of the kind, so a
		// failure can only mean the key itself was rejected.
		value := ""
		switch field.kind {
		case configFieldPort:
			value = "0"
		case configFieldConfirm:
			value = "true"
		}
		if _, err := store.ConfigureValue("demo", field.key, value); err != nil {
			t.Errorf("ConfigureValue(%q, %q) error = %v, want key accepted", field.key, value, err)
		}
	}
}

func TestConfigFormChanges(t *testing.T) {
	fields := []*configFormField{
		configInputField("group", "tools.php", "PHP", "", "8.3"),
		configPortField("group", "server.port", "Port", "", 8080),
		configConfirmField("group", "https", "HTTPS", "", false),
		configInputField("group", "framework", "Framework", "", "laravel"),
	}

	if changes := configFormChanges(fields); len(changes) != 0 {
		t.Fatalf("configFormChanges(unedited) = %v, want none", changes)
	}

	fields[0].value = "8.4"
	fields[1].value = "" // cleared port must unset via "0"
	fields[2].value = "true"

	changes := configFormChanges(fields)
	want := []configChange{
		{key: "tools.php", value: "8.4"},
		{key: "server.port", value: "0"},
		{key: "https", value: "true"},
	}
	if len(changes) != len(want) {
		t.Fatalf("configFormChanges() = %v, want %v", changes, want)
	}
	for i, change := range want {
		if changes[i] != change {
			t.Errorf("configFormChanges()[%d] = %v, want %v", i, changes[i], change)
		}
	}
}

func TestValidateConfigFormPort(t *testing.T) {
	tests := []struct {
		value   string
		wantErr bool
	}{
		{value: "", wantErr: false},
		{value: " ", wantErr: false},
		{value: "0", wantErr: false},
		{value: "1", wantErr: false},
		{value: "8080", wantErr: false},
		{value: "65535", wantErr: false},
		{value: "65536", wantErr: true},
		{value: "-1", wantErr: true},
		{value: "abc", wantErr: true},
		{value: "80.80", wantErr: true},
	}
	for _, test := range tests {
		if err := validateConfigFormPort(test.value); (err != nil) != test.wantErr {
			t.Errorf("validateConfigFormPort(%q) error = %v, wantErr = %v", test.value, err, test.wantErr)
		}
	}
}

func TestApplyConfigFormChangesAppliesAllChanges(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	changes := []configChange{
		{key: "tools.php", value: "8.4"},
		{key: "server.port", value: "8080"},
	}
	applied, err := applyConfigFormChanges(stdout, stderr, store, "demo", changes)
	if err != nil {
		t.Fatalf("applyConfigFormChanges() error = %v, stderr = %q", err, stderr.String())
	}
	if applied != 2 {
		t.Fatalf("applyConfigFormChanges() applied = %d, want 2", applied)
	}

	output := stdout.String()
	for _, expected := range []string{
		"Configured demo\ttools.php=8.4",
		"Configured demo\tserver.port=8080",
		"Applied 2 of 2 change(s).",
	} {
		if !strings.Contains(output, expected) {
			t.Errorf("applyConfigFormChanges() stdout = %q, want %q", output, expected)
		}
	}

	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.PHP != "8.4" {
		t.Errorf("stored php = %q, want %q", environment.PHP, "8.4")
	}
	if environment.Server == nil || environment.Server.Port != 8080 {
		t.Errorf("stored server = %+v, want port 8080", environment.Server)
	}
}

func TestApplyConfigFormChangesContinuesAfterFailure(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	t.Setenv("POLKA_CACHE_DIR", filepath.Join(projectDir, "global-cache"))
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	store, err := resolveStore(root)
	if err != nil {
		t.Fatalf("resolveStore() error = %v", err)
	}

	changes := []configChange{
		{key: "tools.php", value: "8.4"},
		{key: "bogus.key", value: "x"},
		{key: "https", value: "true"},
	}
	applied, err := applyConfigFormChanges(stdout, stderr, store, "demo", changes)
	if applied != 2 {
		t.Fatalf("applyConfigFormChanges() applied = %d, want 2, stderr = %q", applied, stderr.String())
	}

	var status *statusError
	if !errors.As(err, &status) || status.code != 1 {
		t.Fatalf("applyConfigFormChanges() error = %v, want statusError with code 1", err)
	}
	if !strings.Contains(stderr.String(), "bogus.key") {
		t.Errorf("applyConfigFormChanges() stderr = %q, want failing key reported", stderr.String())
	}
	if !strings.Contains(stdout.String(), "Applied 2 of 3 change(s).") {
		t.Errorf("applyConfigFormChanges() stdout = %q, want partial apply summary", stdout.String())
	}

	// The changes after the failing one must still be stored.
	environment := readTestEnvironmentConfig(t, projectDir, "demo")
	if environment.PHP != "8.4" {
		t.Errorf("stored php = %q, want %q", environment.PHP, "8.4")
	}
	if !environment.HTTPS {
		t.Errorf("stored https = false, want true")
	}
}

func TestApplyConfigFormChangesReportsNoChanges(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	applied, err := applyConfigFormChanges(stdout, stderr, backend.Store{}, "demo", nil)
	if err != nil || applied != 0 {
		t.Fatalf("applyConfigFormChanges(no changes) = %d, %v, want 0, nil", applied, err)
	}
	if !strings.Contains(stdout.String(), "No changes.") {
		t.Errorf("applyConfigFormChanges() stdout = %q, want %q", stdout.String(), "No changes.")
	}
}

// TestRunConfigWithoutArgsRequiresTerminal covers the non-TTY guard: buffers
// are not terminals, so the interactive form must refuse with guidance.
func TestRunConfigWithoutArgsRequiresTerminal(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config"}); code != 1 {
		t.Fatalf("Run(config) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "interactive config requires a terminal") {
		t.Errorf("Run(config) stderr = %q, want terminal requirement message", stderr.String())
	}
}

func TestRunConfigRejectsSingleArgument(t *testing.T) {
	projectDir := t.TempDir()
	root := filepath.Join(projectDir, ".polka")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"--root", root, "config", "tools.php"}); code != 1 {
		t.Fatalf("Run(config tools.php) code = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "config requires a key and value, or no arguments for the interactive form") {
		t.Errorf("Run(config tools.php) stderr = %q, want argument guidance", stderr.String())
	}
}
