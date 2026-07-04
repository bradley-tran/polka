package plugins

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectFrameworkFromPackage verifies well-known create-project package
// prefixes map to built-in framework IDs, ignoring version constraints.
func TestDetectFrameworkFromPackage(t *testing.T) {
	cases := map[string]string{
		"laravel/laravel":            Laravel,
		"Laravel/Laravel:^12.0":      Laravel,
		"cakephp/app":                CakePHP,
		"drupal/recommended-project": Drupal,
		"symfony/skeleton":           Symfony,
		"codeigniter4/appstarter":    CodeIgniter,
		"roots/bedrock":              WordPress,
		"johnpbloch/wordpress":       WordPress,
		"acme/blog":                  "",
	}
	for pkg, want := range cases {
		if got := DetectFramework(pkg, t.TempDir()); got != want {
			t.Fatalf("DetectFramework(%q) = %q, want %q", pkg, got, want)
		}
	}
}

// TestDetectFrameworkFromMarkerFiles verifies discriminating scaffold files
// identify the framework when the package name is not distinctive, and that
// a bare composer.json never counts as a framework signal.
func TestDetectFrameworkFromMarkerFiles(t *testing.T) {
	writeMarker := func(t *testing.T, dir string, parts ...string) {
		t.Helper()
		path := filepath.Join(append([]string{dir}, parts...)...)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte("marker\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}

	cases := []struct {
		name    string
		markers [][]string
		want    string
	}{
		{"laravel", [][]string{{"artisan"}}, Laravel},
		{"cakephp", [][]string{{"bin", "cake"}}, CakePHP},
		{"codeigniter", [][]string{{"spark"}}, CodeIgniter},
		{"wordpress", [][]string{{"wp-load.php"}}, WordPress},
		{"drupal", [][]string{{"web", "sites", "default", "default.settings.php"}}, Drupal},
		{"symfony", [][]string{{"symfony.lock"}, {"bin", "console"}}, Symfony},
		{"composer-json-only", [][]string{{"composer.json"}}, ""},
		{"empty", nil, ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, marker := range testCase.markers {
				writeMarker(t, dir, marker...)
			}
			if got := DetectFramework("acme/blog", dir); got != testCase.want {
				t.Fatalf("DetectFramework(acme/blog, %s markers) = %q, want %q", testCase.name, got, testCase.want)
			}
		})
	}
}
