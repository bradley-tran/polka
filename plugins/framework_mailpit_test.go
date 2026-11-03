package plugins

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"polka/config"
)

// TestFrameworkMailpitPortBinding checks configured and default ports without
// database credentials, including repeated calls through the same plugin.
func TestFrameworkMailpitPortBinding(t *testing.T) {
	m, err := parseFrameworkManifest([]byte(`
id: demo
defaults: {docroot: public}
runtime-env:
  values:
    SMTP_PORT: {from: mailpit.smtp-port, default: 1025}
`))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := m.toPlugin()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		port int
		want string
	}{
		{name: "default", want: "1025"},
		{name: "custom", port: 1125, want: "1125"},
		{name: "default after custom", want: "1025"},
	} {
		t.Run(test.name, func(t *testing.T) {
			environment := config.Environment{Settings: config.SettingsConfig{"mailpit": {"smtp-port": test.port}}}
			values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{Environment: environment})
			if got := values["SMTP_PORT"]; got != test.want {
				t.Fatalf("SMTP_PORT = %q, want %q", got, test.want)
			}
		})
	}
}

// TestDrupalPostComposerMailpitSettings evaluates only generated PHP, never
// application code. It verifies effective overrides for both module versions,
// preservation of unrelated settings, and removal after Mailpit is disabled.
// PHP is optional for Go-only development hosts; goldens still pin the output.
func TestDrupalPostComposerMailpitSettings(t *testing.T) {
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skipf("PHP is unavailable: %v", err)
	}
	plugin := newManifestFrameworkPlugin(Drupal)
	ctx := PostComposerContext{
		ProjectDir:  t.TempDir(),
		Environment: plugin.Defaults(),
		Database:    frameworkScriptTestDatabase(),
	}
	settingsPath := filepath.Join(ctx.ProjectDir, "web", "sites", "default", "settings.polka.php")
	for _, test := range []struct {
		name    string
		port    int
		enabled bool
		https   bool
	}{
		{name: "default port", enabled: true},
		{name: "custom port and TLS", port: 1125, enabled: true, https: true},
		{name: "removed Mailpit", port: 1125},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx.Environment.Tools = config.ToolsConfig{}
			if test.enabled {
				ctx.Environment.Tools["mailpit"] = "1.30"
			}
			ctx.Environment.Settings = config.SettingsConfig{"mailpit": {"smtp-port": test.port}}
			ctx.Environment.HTTPS = test.https
			if err := plugin.PostComposer(ctx); err != nil {
				t.Fatal(err)
			}
			generated := string(readPluginTestFile(t, settingsPath))
			if err := plugin.PostComposer(ctx); err != nil {
				t.Fatal(err)
			}
			if again := string(readPluginTestFile(t, settingsPath)); again != generated {
				t.Fatal("repeated Composer hook changed generated settings")
			}
			// Seed a saved sendmail command so replacement must discard its
			// query options as well as select the SMTP plugin.
			code := `
$config = ['unrelated.settings' => ['keep' => true]];
foreach (['symfony_mailer', 'mailer_transport'] as $prefix) {
  $config[$prefix . '.settings'] = ['default_transport' => 'production', 'keep' => true];
  $config[$prefix . '.mailer_transport.sendmail'] = [
    'plugin' => 'sendmail',
    'configuration' => ['query' => ['command' => '/usr/sbin/sendmail -bs']],
  ];
}
require $argv[1];
echo json_encode($config, JSON_THROW_ON_ERROR);
`
			output, err := exec.Command(php, "-n", "-r", code, settingsPath).CombinedOutput()
			if err != nil {
				t.Fatalf("evaluate generated PHP: %v\n%s", err, output)
			}
			var got map[string]any
			if err := json.Unmarshal(output, &got); err != nil {
				t.Fatalf("decode PHP configuration: %v\n%s", err, output)
			}
			want := map[string]any{"unrelated.settings": map[string]any{"keep": true}}
			port := test.port
			if port == 0 {
				port = 1025
			}
			for _, prefix := range []string{"symfony_mailer", "mailer_transport"} {
				defaultTransport := "production"
				if test.enabled {
					defaultTransport = "sendmail"
				}
				want[prefix+".settings"] = map[string]any{"default_transport": defaultTransport, "keep": true}
				transport := map[string]any{
					"plugin":        "sendmail",
					"configuration": map[string]any{"query": map[string]any{"command": "/usr/sbin/sendmail -bs"}},
				}
				if test.enabled {
					transport = map[string]any{
						"plugin": "smtp",
						"configuration": map[string]any{
							"user": "", "pass": "", "host": "127.0.0.1", "port": float64(port),
							"query": map[string]any{"verify_peer": false},
						},
					}
				}
				want[prefix+".mailer_transport.sendmail"] = transport
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("effective mail configuration = %#v, want %#v", got, want)
			}
		})
	}
}
