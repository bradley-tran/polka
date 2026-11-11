package plugins

import (
	"strings"
	"testing"

	"polka/config"
)

// TestRuntimeEnvServiceValuesIgnoreDatabaseGuard verifies service groups are
// exported for configured services even when requires-database suppresses
// the general values, and are skipped for services without resolved values.
func TestRuntimeEnvServiceValuesIgnoreDatabaseGuard(t *testing.T) {
	m, err := parseFrameworkManifest([]byte(`
id: demo
defaults: {docroot: public}
runtime-env:
  requires-database: true
  values:
    DB_PORT: {from: database.port}
  service-values:
    redis:
      REDIS_PORT: {from: redis.port}
      REDIS_PASSWORD: {from: redis.password, default: "null"}
    meilisearch:
      MEILISEARCH_HOST: {from: meilisearch.url}
`))
	if err != nil {
		t.Fatal(err)
	}
	plugin, err := m.toPlugin()
	if err != nil {
		t.Fatal(err)
	}

	values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{
		ServiceValues: map[string]string{"redis.host": "127.0.0.1", "redis.port": "21003", "redis.password": ""},
	})
	if values["REDIS_PORT"] != "21003" || values["REDIS_PASSWORD"] != "null" {
		t.Fatalf("RuntimeEnv() = %#v, want leased redis port and null password default", values)
	}
	if _, ok := values["DB_PORT"]; ok {
		t.Fatalf("RuntimeEnv() = %#v, want database-guarded values omitted", values)
	}
	if _, ok := values["MEILISEARCH_HOST"]; ok {
		t.Fatalf("RuntimeEnv() = %#v, want unconfigured meilisearch group omitted", values)
	}

	empty, err := plugin.RuntimeEnv(RuntimeEnvContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("RuntimeEnv(no services) = %#v, want nothing", empty)
	}
}

// TestRuntimeEnvMailpitPortPrefersResolvedValue verifies the resolved (leased)
// SMTP port wins over the configured setting.
func TestRuntimeEnvMailpitPortPrefersResolvedValue(t *testing.T) {
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
	environment := config.Environment{Settings: config.SettingsConfig{"mailpit": {"smtp-port": 1125}}}
	values := runtimeEnvForTest(t, plugin, RuntimeEnvContext{
		Environment:   environment,
		ServiceValues: map[string]string{"mailpit.smtp-port": "21001"},
	})
	if values["SMTP_PORT"] != "21001" {
		t.Fatalf("SMTP_PORT = %q, want resolved 21001", values["SMTP_PORT"])
	}
}

// TestServiceValuesManifestValidation rejects unknown service groups and
// duplicate variables across groups.
func TestServiceValuesManifestValidation(t *testing.T) {
	for _, test := range []struct {
		name     string
		manifest string
		want     string
	}{
		{
			name: "unknown service",
			manifest: `
id: demo
defaults: {docroot: public}
runtime-env:
  service-values:
    memcached:
      MEMCACHED_PORT: {from: redis.port}
`,
			want: "unsupported service-values service",
		},
		{
			name: "duplicate variable",
			manifest: `
id: demo
defaults: {docroot: public}
runtime-env:
  values:
    REDIS_PORT: "6379"
  service-values:
    redis:
      REDIS_PORT: {from: redis.port}
`,
			want: "duplicate runtime variable name",
		},
		{
			name: "unknown service reference",
			manifest: `
id: demo
defaults: {docroot: public}
runtime-env:
  service-values:
    redis:
      REDIS_DB: {from: redis.database}
`,
			want: "unknown reference",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, err := parseFrameworkManifest([]byte(test.manifest))
			if err == nil {
				_, err = m.toPlugin()
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("manifest error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestLaravelAndSymfonyServiceValues verifies the built-in manifests point
// applications at the resolved managed service endpoints.
func TestLaravelAndSymfonyServiceValues(t *testing.T) {
	services := map[string]string{
		"mailpit.smtp-host": "127.0.0.1", "mailpit.smtp-port": "21001",
		"redis.host": "127.0.0.1", "redis.port": "21002", "redis.password": "secret",
		"meilisearch.url": "http://127.0.0.1:21003", "meilisearch.key": "key",
	}

	laravel := runtimeEnvForTest(t, newManifestFrameworkPlugin(Laravel), RuntimeEnvContext{ServiceValues: services})
	for key, want := range map[string]string{
		"MAIL_HOST": "127.0.0.1", "MAIL_PORT": "21001",
		"REDIS_HOST": "127.0.0.1", "REDIS_PORT": "21002", "REDIS_PASSWORD": "secret",
		"MEILISEARCH_HOST": "http://127.0.0.1:21003", "MEILISEARCH_KEY": "key",
	} {
		if laravel[key] != want {
			t.Fatalf("laravel %s = %q, want %q", key, laravel[key], want)
		}
	}

	symfony := runtimeEnvForTest(t, newManifestFrameworkPlugin(Symfony), RuntimeEnvContext{ServiceValues: services})
	for key, want := range map[string]string{
		"MAILER_DSN":          "smtp://127.0.0.1:21001/",
		"MEILISEARCH_URL":     "http://127.0.0.1:21003",
		"MEILISEARCH_API_KEY": "key",
	} {
		if symfony[key] != want {
			t.Fatalf("symfony %s = %q, want %q", key, symfony[key], want)
		}
	}
}
