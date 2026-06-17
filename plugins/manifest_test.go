package plugins

import (
	"strings"
	"testing"
)

func TestBuiltinFrameworkManifestsLoad(t *testing.T) {
	for _, framework := range []string{CodeIgniter, Drupal, WordPress, Laravel, Symfony} {
		t.Run(framework, func(t *testing.T) {
			manifest, err := loadBuiltinFrameworkManifest(framework)
			if err != nil {
				t.Fatalf("loadBuiltinFrameworkManifest(%s) error = %v", framework, err)
			}
			if manifest.ID != framework {
				t.Fatalf("loadBuiltinFrameworkManifest(%s) id = %q, want %q", framework, manifest.ID, framework)
			}
			if manifest.Defaults.Framework != framework {
				t.Fatalf("manifest defaults.framework = %q, want %q", manifest.Defaults.Framework, framework)
			}
		})
	}
}

func TestParseFrameworkManifestRequiresID(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
defaults:
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "id cannot be empty") {
		t.Fatalf("parseFrameworkManifest(missing id) error = %v, want missing id error", err)
	}
}

func TestParseFrameworkManifestRejectsInvalidID(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: "not a framework"
defaults:
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "invalid framework manifest id") {
		t.Fatalf("parseFrameworkManifest(invalid id) error = %v, want invalid id error", err)
	}
}

func TestParseFrameworkManifestRejectsDefaultFrameworkMismatch(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: laravel
defaults:
  framework: symfony
  docroot: public
`))
	if err == nil || !strings.Contains(err.Error(), "defaults.framework must match id") {
		t.Fatalf("parseFrameworkManifest(framework mismatch) error = %v, want mismatch error", err)
	}
}

func TestParseFrameworkManifestRequiresDocroot(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  tools:
    php: "8.4"
`))
	if err == nil || !strings.Contains(err.Error(), "defaults.docroot cannot be empty") {
		t.Fatalf("parseFrameworkManifest(missing docroot) error = %v, want docroot error", err)
	}
}

func TestParseFrameworkManifestRejectsInvalidRuntimeEnvStrategy(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
runtime-env:
  database:
    keys: rails
`))
	if err == nil || !strings.Contains(err.Error(), "runtime-env.database.keys has unsupported value") {
		t.Fatalf("parseFrameworkManifest(invalid runtime strategy) error = %v, want strategy error", err)
	}
}

func TestParseFrameworkManifestRejectsInvalidPostComposerStrategy(t *testing.T) {
	_, err := parseFrameworkManifest([]byte(`
id: demo
defaults:
  docroot: public
post-composer:
  strategy: shell
`))
	if err == nil || !strings.Contains(err.Error(), "post-composer.strategy has unsupported value") {
		t.Fatalf("parseFrameworkManifest(invalid post composer strategy) error = %v, want strategy error", err)
	}
}

func TestParseFrameworkManifestRejectsUnsafePostComposerPaths(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "app root escape",
			yaml: `
id: demo
defaults:
  docroot: public
post-composer:
  strategy: dotenv
  app-root:
    public-dir: ../public
  dotenv:
    target: .env
`,
			wantErr: "post-composer.app-root.public-dir",
		},
		{
			name: "dotenv target escape",
			yaml: `
id: demo
defaults:
  docroot: public
post-composer:
  strategy: dotenv
  app-root:
    public-dir: public
  dotenv:
    target: ../.env
`,
			wantErr: "post-composer.dotenv.target",
		},
		{
			name: "dotenv template absolute",
			yaml: `
id: demo
defaults:
  docroot: public
post-composer:
  strategy: dotenv
  app-root:
    public-dir: public
  dotenv:
    target: .env
    template: /tmp/.env.example
`,
			wantErr: "post-composer.dotenv.template",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseFrameworkManifest([]byte(test.yaml))
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("parseFrameworkManifest() error = %v, want %q", err, test.wantErr)
			}
		})
	}
}
