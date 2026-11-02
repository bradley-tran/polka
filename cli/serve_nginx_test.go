package cli

import (
	"errors"
	"strings"
	"testing"

	"polka/backend"
	"polka/plugins"
)

// stubNginxFrameworkPlugin overrides only the nginx hook of a framework plugin.
type stubNginxFrameworkPlugin struct {
	plugins.FrameworkPlugin
	hook func(plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error)
}

// NginxConfig calls the test's hook.
func (p stubNginxFrameworkPlugin) NginxConfig(ctx plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error) {
	return p.hook(ctx)
}

// TestFrameworkNginxServeConfigUsesHookResult verifies a plugin receives the
// generic config and that its answer decides what gets written.
func TestFrameworkNginxServeConfigUsesHookResult(t *testing.T) {
	layout := serveAppLayout{Docroot: "/app/public dir"}
	tlsConfig := serveTLSConfig{Enabled: true, CertificatePath: "/tls/cert.pem", CertificateKeyPath: "/tls/key.pem"}
	generic := string(renderNginxServeConfig("demo.test", 8443, layout, "127.0.0.1:9000", tlsConfig))
	environment := backend.Environment{Name: "default", Framework: "demo"}

	render := func(hook func(plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error)) ([]byte, error) {
		return frameworkNginxServeConfig(stubNginxFrameworkPlugin{hook: hook}, environment, "demo.test", 8443, layout, "127.0.0.1:9000", tlsConfig)
	}

	config, err := render(func(ctx plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error) {
		if ctx.DefaultConfig != generic {
			t.Errorf("DefaultConfig = %q, want the generic config %q", ctx.DefaultConfig, generic)
		}
		if ctx.Host != "demo.test" || ctx.Port != 8443 || ctx.Docroot != layout.Docroot || ctx.BackendAddress != "127.0.0.1:9000" || !ctx.TLSEnabled || ctx.TLSCertificatePath != "/tls/cert.pem" || ctx.TLSCertificateKeyPath != "/tls/key.pem" {
			t.Errorf("context = %#v, want the serve parameters", ctx)
		}
		return plugins.NginxConfigResult{Config: []byte("server {}")}, true, nil
	})
	if err != nil || string(config) != "server {}" {
		t.Fatalf("handled config = %q, %v", config, err)
	}

	config, err = render(func(plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error) {
		return plugins.NginxConfigResult{}, false, nil
	})
	if err != nil || config != nil {
		t.Fatalf("unhandled config = %q, %v, want nil so the caller writes the generic config", config, err)
	}

	hookErr := errors.New("hook failed")
	if _, err = render(func(plugins.NginxConfigContext) (plugins.NginxConfigResult, bool, error) {
		return plugins.NginxConfigResult{}, false, hookErr
	}); !errors.Is(err, hookErr) {
		t.Fatalf("hook error = %v, want %v", err, hookErr)
	}
}

// TestRenderFrameworkNginxServeConfigBuiltins verifies built-in frameworks keep
// the generic config and that an unknown framework is an error.
func TestRenderFrameworkNginxServeConfigBuiltins(t *testing.T) {
	layout := serveAppLayout{Docroot: "public"}
	for _, framework := range []string{"", plugins.CakePHP, plugins.CodeIgniter, plugins.Drupal, plugins.Laravel, plugins.Symfony, plugins.WordPress} {
		config, err := renderFrameworkNginxServeConfig(backend.Environment{Framework: framework}, "localhost", 8080, layout, "127.0.0.1:9000", serveTLSConfig{})
		if err != nil || config != nil {
			t.Fatalf("renderFrameworkNginxServeConfig(%q) = %q, %v, want nil", framework, config, err)
		}
	}

	_, err := renderFrameworkNginxServeConfig(backend.Environment{Framework: "missing"}, "localhost", 8080, layout, "127.0.0.1:9000", serveTLSConfig{})
	if err == nil || !strings.Contains(err.Error(), "unsupported framework") {
		t.Fatalf("renderFrameworkNginxServeConfig(missing) error = %v", err)
	}
}
