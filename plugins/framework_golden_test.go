package plugins

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"polka/config"
	"polka/tools"
)

// goldenPassword carries characters that are special in PHP strings, dotenv
// values, and URLs, so the goldens pin every escaping path.
const goldenPassword = `pa'ss"w$rd\#%20`

// goldenDatabases lists the credentials used per engine. The ports differ so
// the goldens pin both forms of a host-port binding: the default port omitted
// and an explicit port kept.
var goldenDatabases = []DatabaseCredentials{
	{Engine: tools.MySQL, Version: "8.4", Host: "127.0.0.1", Port: 3306, DatabaseName: "golden_db", User: "golden_user", Password: goldenPassword},
	{Engine: tools.MariaDB, Host: "127.0.0.1", Port: 3307, DatabaseName: "golden_db", User: "golden_user", Password: goldenPassword},
	{Engine: tools.PostgreSQL, Version: "17", Host: "127.0.0.1", Port: 5433, DatabaseName: "golden_db", User: "golden_user", Password: goldenPassword},
}

// goldenSeeds holds the application files a framework reads when its target
// does not exist yet. Without them an empty project only reaches embedded
// fallbacks, so a misspelled project fallback path would go unnoticed.
var goldenSeeds = map[string]map[string]string{
	CakePHP: {
		"config/app_local.example.php": "<?php\nreturn [\n    'debug' => true,\n    'Datasources' => [\n        'default' => [\n            'host' => 'localhost',\n            'username' => 'my_app',\n        ],\n    ],\n];\n",
	},
	CodeIgniter: {
		"env": "# CI_ENVIRONMENT = production\n# database.default.hostname = localhost\n",
	},
	Drupal: {
		"web/sites/default/default.settings.php": "<?php\n\n$databases = [];\n",
	},
	Laravel: {
		".env.example": "APP_NAME=Demo\nDB_CONNECTION=sqlite\n# DB_HOST=127.0.0.1\n",
	},
	Symfony: {
		".env.local": "APP_ENV=dev\n",
	},
	WordPress: {
		"wp-config-sample.php": "<?php\ndefine( 'DB_NAME', 'database_name_here' );\ndefine( 'DB_CHARSET', 'utf8' );\n$table_prefix = 'wp_';\nrequire_once ABSPATH . 'wp-settings.php';\n",
	},
}

// TestBuiltinFrameworkPostComposerGolden pins the exact files every built-in
// framework generates, for each supported engine and from project fallbacks.
// Treat a difference as a behavior change. Set POLKA_UPDATE_TESTDATA=1 to
// regenerate the files after an intended change.
func TestBuiltinFrameworkPostComposerGolden(t *testing.T) {
	for _, plugin := range DefaultFrameworkPlugins() {
		seed, ok := goldenSeeds[plugin.ID()]
		if !ok {
			t.Fatalf("framework %q has no golden seed", plugin.ID())
		}
		for _, database := range goldenDatabases {
			t.Run(plugin.ID()+"/"+database.Engine, func(t *testing.T) {
				environment := plugin.Defaults()
				environment.Database = &config.DatabaseConfig{Engine: database.Engine}
				if err := plugin.ValidateEnvironment(environment); err != nil {
					if plugin.ID() != WordPress || database.Engine != tools.PostgreSQL {
						t.Fatalf("ValidateEnvironment() error = %v", err)
					}
					t.Skip("framework does not support this engine")
				}
				checkPostComposerGolden(t, plugin, environment, database, nil, filepath.Join(plugin.ID(), database.Engine))
			})
		}
		t.Run(plugin.ID()+"/seeded", func(t *testing.T) {
			environment := plugin.Defaults()
			environment.Database = &config.DatabaseConfig{Engine: tools.MariaDB}
			checkPostComposerGolden(t, plugin, environment, goldenDatabases[1], seed, filepath.Join(plugin.ID(), "seeded"))
		})
	}
}

// checkPostComposerGolden runs the hook twice in a fresh project, because
// create-project does the same, and compares the result with the named golden
// directory. The second run must not change anything.
func checkPostComposerGolden(t *testing.T, plugin FrameworkPlugin, environment config.Environment, database DatabaseCredentials, seed map[string]string, name string) {
	t.Helper()
	dir := t.TempDir()
	for file, contents := range seed {
		writePluginTestFile(t, filepath.Join(dir, filepath.FromSlash(file)), contents)
	}
	context := PostComposerContext{ProjectDir: dir, Environment: environment, Database: &database}
	if err := plugin.PostComposer(context); err != nil {
		t.Fatalf("PostComposer() error = %v", err)
	}
	got := readGoldenTree(t, dir)
	if err := plugin.PostComposer(context); err != nil {
		t.Fatalf("second PostComposer() error = %v", err)
	}
	if again := readGoldenTree(t, dir); !maps.Equal(got, again) {
		t.Fatalf("second PostComposer() changed output:\nfirst:  %q\nsecond: %q", got, again)
	}

	golden := filepath.Join("testdata", "post-composer", name)
	if os.Getenv("POLKA_UPDATE_TESTDATA") == "1" {
		if err := os.RemoveAll(golden); err != nil {
			t.Fatalf("RemoveAll(%s) error = %v", golden, err)
		}
		for file, contents := range got {
			writePluginTestFile(t, filepath.Join(golden, filepath.FromSlash(file)), contents)
		}
	}
	want := readGoldenTree(t, golden)
	if gotFiles, wantFiles := slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)); !slices.Equal(gotFiles, wantFiles) {
		t.Fatalf("generated files = %q, want %q", gotFiles, wantFiles)
	}
	for file, contents := range got {
		if contents != want[file] {
			t.Errorf("%s differs from golden:\ngot:  %q\nwant: %q", file, contents, want[file])
		}
	}
}

// readGoldenTree returns the files under dir by slash-separated relative path.
// Line endings are normalized because embedded templates follow the checkout's
// line endings on Windows.
func readGoldenTree(t *testing.T, dir string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = strings.ReplaceAll(string(data), "\r\n", "\n")
		return nil
	})
	if err != nil {
		t.Fatalf("read golden tree %s: %v", dir, err)
	}
	return files
}
