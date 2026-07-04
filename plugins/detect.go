package plugins

import (
	"path/filepath"
	"strings"
)

// ComposerCreateProjectDirectory exposes the composer create-project target
// directory parser for the polka create-project command. args must include
// the create-project verb; relative directories resolve against workingDir.
func ComposerCreateProjectDirectory(args []string, workingDir string) string {
	return composerCreateProjectDirectory(args, workingDir)
}

// DetectFramework identifies the built-in framework of a freshly scaffolded
// application from the create-project package name, falling back to
// framework-specific marker files in the target directory. It returns ""
// when nothing distinctive is found; unlike frameworkAppRootLooksLike it
// never treats a bare composer.json as a framework signal.
func DetectFramework(pkg, dir string) string {
	if framework := frameworkFromComposerPackage(pkg); framework != "" {
		return framework
	}

	return frameworkFromMarkerFiles(dir)
}

// frameworkFromComposerPackage maps well-known create-project package names
// to built-in framework IDs.
func frameworkFromComposerPackage(pkg string) string {
	name := strings.ToLower(strings.TrimSpace(pkg))
	if colon := strings.Index(name, ":"); colon >= 0 {
		name = name[:colon]
	}

	switch {
	case strings.HasPrefix(name, "laravel/"):
		return Laravel
	case strings.HasPrefix(name, "cakephp/"):
		return CakePHP
	case strings.HasPrefix(name, "drupal/"):
		return Drupal
	case strings.HasPrefix(name, "symfony/"):
		return Symfony
	case strings.HasPrefix(name, "codeigniter4/"):
		return CodeIgniter
	case strings.HasPrefix(name, "roots/") || strings.Contains(name, "wordpress"):
		return WordPress
	default:
		return ""
	}
}

// frameworkFromMarkerFiles checks discriminating files that only one
// framework scaffolds.
func frameworkFromMarkerFiles(dir string) string {
	switch {
	case regularFileExists(filepath.Join(dir, "artisan")):
		return Laravel
	case regularFileExists(filepath.Join(dir, "bin", "cake")):
		return CakePHP
	case regularFileExists(filepath.Join(dir, "spark")):
		return CodeIgniter
	case regularFileExists(filepath.Join(dir, "wp-load.php")) || regularFileExists(filepath.Join(dir, "wp-config-sample.php")):
		return WordPress
	case regularFileExists(filepath.Join(dir, defaultDrupalDocroot, "sites", "default", "default.settings.php")):
		return Drupal
	case regularFileExists(filepath.Join(dir, "symfony.lock")) || regularFileExists(filepath.Join(dir, "bin", "console")):
		return Symfony
	default:
		return ""
	}
}
