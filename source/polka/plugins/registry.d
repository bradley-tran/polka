module polka.plugins.registry;

import std.algorithm : canFind, sort;
import std.format : format;
import std.string : endsWith, indexOf, startsWith, strip, toLower;

import polka.config;
import polka.plugins.ids;
import polka.plugins.framework_composer;
import polka.plugins.manifest;
import polka.plugins.preset;
import polka.tools : ToolPlugin, ToolRegistry = Registry, newDefaultToolRegistry = newDefaultRegistry;

/// Owns built-in and registered Polka plugins grouped by plugin kind (`tools`, `frameworks`, `presets`).
class Registry
{
    private ToolRegistry tools;
    private FrameworkPlugin[] frameworksList;
    private FrameworkPlugin[string] frameworksByID;
    private ProjectPreset[] presetsList;
    private ProjectPreset[string] presetsByID;

    this(ToolRegistry toolRegistry, FrameworkPlugin[] frameworkPlugins...)
    {
        tools = toolRegistry !is null ? toolRegistry : newDefaultToolRegistry();
        foreach (plugin; frameworkPlugins)
            registerFramework(plugin);
    }

    /// Returns the installable tool registry.
    ToolRegistry toolRegistry()
    {
        if (tools is null)
            return newDefaultToolRegistry();
        return tools;
    }

    /// Registers a framework plugin, enforcing unique non-colliding valid IDs.
    void registerFramework(FrameworkPlugin plugin)
    {
        if (plugin is null)
            throw new Exception("framework plugin cannot be nil");

        auto rawId = plugin.id();
        auto id = rawId.strip().toLower();
        if (id.length == 0)
            throw new Exception("framework plugin id cannot be empty");
        if (!isValidPluginID(id))
            throw new Exception(format("invalid framework plugin id %s: use letters, numbers, dots, dashes, or underscores",
                formatGoQuoted(rawId)));
        if ((id in frameworksByID) !is null)
            throw new Exception(format("duplicate framework plugin %s", formatGoQuoted(id)));
        if ((id in presetsByID) !is null)
            throw new Exception(format("plugin id %s is already registered as a project preset", formatGoQuoted(id)));

        frameworksList ~= plugin;
        frameworksByID[id] = plugin;
    }

    /// Registers a project preset, enforcing unique non-colliding valid IDs.
    void registerPreset(ProjectPreset preset)
    {
        if (preset is null)
            throw new Exception("project preset cannot be nil");

        auto rawId = preset.id();
        auto id = rawId.strip().toLower();
        if (id.length == 0)
            throw new Exception("project preset id cannot be empty");
        if (!isValidPluginID(id))
            throw new Exception(format("invalid project preset id %s: use letters, numbers, dots, dashes, or underscores",
                formatGoQuoted(rawId)));
        if ((id in presetsByID) !is null)
            throw new Exception(format("duplicate project preset %s", formatGoQuoted(id)));
        if ((id in frameworksByID) !is null)
            throw new Exception(format("plugin id %s is already registered as a framework", formatGoQuoted(id)));

        presetsList ~= preset;
        presetsByID[id] = preset;
    }

    /// Resolves a project preset by ID.
    bool preset(string id, out ProjectPreset outPreset) const
    {
        auto p = id.strip().toLower() in presetsByID;
        if (p is null)
        {
            outPreset = null;
            return false;
        }
        outPreset = cast(ProjectPreset) *p;
        return true;
    }

    /// Returns registered project presets in registration order.
    ProjectPreset[] presets() const
    {
        if (presetsList.length == 0)
            return null;
        return (cast(ProjectPreset[]) presetsList).dup;
    }

    /// Returns project preset IDs in stable sorted order.
    string[] supportedPresets() const
    {
        string[] ids;
        ids.reserve(presetsList.length);
        foreach (p; presetsList)
            ids ~= p.id().strip().toLower();
        ids.sort();
        return ids;
    }

    /// Resolves a framework plugin by ID.
    bool framework(string id, out FrameworkPlugin outPlugin) const
    {
        auto p = id.strip().toLower() in frameworksByID;
        if (p is null)
        {
            outPlugin = null;
            return false;
        }
        outPlugin = cast(FrameworkPlugin) *p;
        return true;
    }

    /// Returns registered framework plugins in registration order.
    FrameworkPlugin[] frameworks() const
    {
        if (frameworksList.length == 0)
            return null;
        return (cast(FrameworkPlugin[]) frameworksList).dup;
    }

    /// Returns framework IDs in stable sorted order.
    string[] supportedFrameworks() const
    {
        string[] ids;
        ids.reserve(frameworksList.length);
        foreach (p; frameworksList)
            ids ~= p.id().strip().toLower();
        ids.sort();
        return ids;
    }

    /// Checks whether a non-empty framework ID is registered.
    void validateFramework(string id) const
    {
        auto trimmed = id.strip().toLower();
        if (trimmed.length == 0)
            return;
        FrameworkPlugin dummy;
        if (!framework(trimmed, dummy))
        {
            import std.array : join;
            throw new Exception(format("unsupported framework %s; supported frameworks: %s",
                formatGoQuoted(id), supportedFrameworks().join(", ")));
        }
    }

    /// Validates framework-specific compatibility constraints for an `Environment`.
    void validateEnvironment(const Environment environment) const
    {
        validateFramework(environment.framework);
        if (environment.framework.strip().length == 0)
            return;
        FrameworkPlugin plugin;
        framework(environment.framework, plugin);
        plugin.validateEnvironment(environment);
    }

    /// Detects framework ID using only the active registry.
    /// Category order: `package-prefixes` -> `package-substrings` -> `markers`.
    /// Ties break by descending `priority`, then ascending `id`.
    /// Missing marker files are non-matches; filesystem errors are propagated.
    string detectFramework(string pkg, string dir) const
    {
        auto name = pkg.strip().toLower();
        auto colon = name.indexOf(':');
        if (colon >= 0)
            name = name[0 .. colon];

        auto fws = frameworks();
        fws.sort!((a, b) {
            auto ap = a.detection().priority;
            auto bp = b.detection().priority;
            if (ap != bp)
                return ap > bp;
            return a.id() < b.id();
        });

        foreach (category; ["prefix", "substring", "marker"])
        {
            foreach (fw; fws)
            {
                auto d = fw.detection();
                switch (category)
                {
                    case "prefix":
                        foreach (pattern; d.packagePrefixes)
                        {
                            if (name.startsWith(pattern.toLower()))
                                return fw.id();
                        }
                        break;
                    case "substring":
                        foreach (pattern; d.packageSubstrings)
                        {
                            if (name.canFind(pattern.toLower()))
                                return fw.id();
                        }
                        break;
                    case "marker":
                        try
                        {
                            if (frameworkMarkersExist(dir, d.markers))
                                return fw.id();
                        }
                        catch (Exception e)
                        {
                            throw new Exception(format("detect framework %s: %s", formatGoQuoted(fw.id()), e.msg));
                        }
                        break;
                    default:
                        break;
                }
            }
        }
        return "";
    }
}

/// Builds a plugin registry from a tool registry and framework plugins.
Registry newRegistry(ToolRegistry toolRegistry, FrameworkPlugin[] frameworkPlugins...)
{
    return new Registry(toolRegistry, frameworkPlugins);
}

/// Returns Polka's built-in tool, framework, and preset registry.
Registry newDefaultRegistry()
{
    auto registry = newRegistry(newDefaultToolRegistry(), defaultFrameworkPlugins());
    foreach (p; defaultProjectPresets())
        registry.registerPreset(p);
    return registry;
}

// ============================================================================
// Comprehensive Unit Tests Ported from Go `plugins/*_test.go`
// ============================================================================
unittest
{
    import std.algorithm : count;
    import std.file : exists, mkdirRecurse, read, readText, write;
    import std.path : buildPath, dirName;
    import polka.plugins.framework_actions;
    import polka.plugins.framework_php;
    import polka.plugins.manifest_values;
    import polka.support.fs : createTempDir, fromSlashPath, getFileMode, isSymlinkPath, octal, rmdirRecurseQuiet, setFileMode;
    import polka.tools : MariaDB, MySQL, PostgreSQL, Composer, NodeJS, Nginx, PHP;

    static void writeTestFile(string filename, string text)
    {
        auto parent = dirName(filename);
        if (!exists(parent))
            mkdirRecurse(parent);
        write(filename, text);
    }

    // ------------------------------------------------------------------------
    // registry_test.go
    // ------------------------------------------------------------------------
    {
        auto reg = newDefaultRegistry();
        assert(reg.supportedFrameworks() == [CakePHP, CodeIgniter, Drupal, Laravel, Symfony, WordPress]);
        foreach (id; [CakePHP, CodeIgniter, Drupal, WordPress, Laravel, Symfony])
        {
            FrameworkPlugin fw;
            assert(reg.framework(id, fw) && fw !is null);
        }
        bool threw = false;
        try { reg.validateFramework("yii"); } catch (Exception) { threw = true; }
        assert(threw);

        assert(reg.supportedPresets() == [PHPExtension]);
        ProjectPreset pr;
        assert(reg.preset(PHPExtension, pr));
        FrameworkPlugin fwExt;
        assert(!reg.framework(PHPExtension, fwExt));
    }

    // TestRegistryRejectsDuplicateAndCollidingProjectPresets & DuplicateFrameworkPlugins
    {
        auto reg = newRegistry(newDefaultToolRegistry());
        auto preset = defaultProjectPresets()[0];
        reg.registerPreset(preset);
        bool threwDup = false;
        try { reg.registerPreset(preset); } catch (Exception) { threwDup = true; }
        assert(threwDup);

        auto fw = defaultFrameworkPlugins()[0];
        reg = newRegistry(newDefaultToolRegistry(), fw);
        auto colliding = new BuiltinProjectPreset(fw.id());
        bool threwCol1 = false;
        try { reg.registerPreset(colliding); } catch (Exception) { threwCol1 = true; }
        assert(threwCol1);

        reg = newRegistry(newDefaultToolRegistry());
        reg.registerPreset(colliding);
        bool threwCol2 = false;
        try { reg.registerFramework(fw); } catch (Exception) { threwCol2 = true; }
        assert(threwCol2);

        bool threwDupFw = false;
        try { newRegistry(newDefaultToolRegistry(), fw, fw); } catch (Exception) { threwDupFw = true; }
        assert(threwDupFw);
    }

    // TestFrameworkDefaultsUseExpectedPresetMatrix
    {
        auto reg = newDefaultRegistry();
        void assertExts(const bool[string] exts, string[] expected)
        {
            foreach (e; expected)
                assert((e in exts) !is null && exts[e], "missing extension " ~ e);
        }

        FrameworkPlugin cakePHP, ci, drupal, laravel, symfony, wp;
        assert(reg.framework(CakePHP, cakePHP));
        auto cakeDef = cakePHP.defaults();
        assert(cakeDef.framework == CakePHP && cakeDef.docroot == "webroot"
            && cakeDef.toolVersion(Composer) == "2.8" && cakeDef.toolVersion(NodeJS) == "24"
            && cakeDef.mailpit() !is null && cakeDef.opcachePreset == OPcachePresetDev
            && cakeDef.opcacheConfig.length == 0 && cakeDef.phpExtensions.length == 0);
        assertExts(cakePHP.phpExtensions(), ["intl", "mbstring", "opcache", "openssl", "simplexml"]);

        assert(reg.framework(CodeIgniter, ci));
        auto ciDef = ci.defaults();
        assert(ciDef.framework == CodeIgniter && ciDef.docroot == "public"
            && ciDef.toolVersion(Composer) == "2.8" && ciDef.toolVersion(NodeJS) == "24"
            && ciDef.mailpit() !is null && ciDef.opcachePreset == OPcachePresetDev
            && ciDef.opcacheConfig.length == 0 && ciDef.phpExtensions.length == 0);
        assertExts(ci.phpExtensions(), ["curl", "fileinfo", "intl", "mbstring", "opcache", "openssl", "zip"]);

        assert(reg.framework(Drupal, drupal));
        auto drupalDef = drupal.defaults();
        assert(drupalDef.framework == Drupal && drupalDef.docroot == "web"
            && drupalDef.toolVersion(Composer) == "2.8" && drupalDef.toolVersion(NodeJS) == "24"
            && drupalDef.mailpit() !is null && drupalDef.opcachePreset == OPcachePresetDev
            && drupalDef.opcacheConfig["opcache.save_comments"] == "1"
            && drupalDef.phpExtensions.length == 0);
        assertExts(drupal.phpExtensions(), ["curl", "dom", "fileinfo", "gd", "intl", "mbstring", "opcache", "openssl", "pdo_sqlite", "simplexml", "sqlite3", "xmlreader", "xsl", "zip", "zlib"]);

        assert(reg.framework(Laravel, laravel));
        auto laraDef = laravel.defaults();
        assert(laraDef.framework == Laravel && laraDef.docroot == "public"
            && laraDef.toolVersion(Composer) == "2.8" && laraDef.toolVersion(NodeJS) == "24"
            && laraDef.mailpit() !is null && laraDef.opcachePreset == OPcachePresetDev
            && laraDef.opcacheConfig.length == 0 && laraDef.phpExtensions.length == 0);
        assertExts(laravel.phpExtensions(), ["bcmath", "curl", "dom", "fileinfo", "gd", "intl", "mbstring", "opcache", "openssl", "pdo_sqlite", "simplexml", "sqlite3", "xmlreader", "xsl", "zip"]);

        assert(reg.framework(Symfony, symfony));
        auto symDef = symfony.defaults();
        assert(symDef.framework == Symfony && symDef.docroot == "public"
            && symDef.toolVersion(Composer) == "2.8" && symDef.toolVersion(NodeJS) == "24"
            && symDef.mailpit() !is null && symDef.opcachePreset == OPcachePresetDev
            && symDef.opcacheConfig.length == 0 && symDef.phpExtensions.length == 0);
        assertExts(symfony.phpExtensions(), ["ctype", "curl", "dom", "fileinfo", "gd", "iconv", "intl", "mbstring", "opcache", "openssl", "pdo_sqlite", "session", "simplexml", "sqlite3", "tokenizer", "xmlreader", "zip"]);

        assert(reg.framework(WordPress, wp));
        auto wpDef = wp.defaults();
        assert(wpDef.framework == WordPress && wpDef.docroot == "."
            && wpDef.toolVersion(Composer) == "" && wpDef.toolVersion(NodeJS) == ""
            && wpDef.mailpit() is null && wpDef.toolVersion(PHP) == "8.4"
            && wpDef.toolVersion(Nginx) == "1.30" && wpDef.toolVersion(MariaDB) == "11.8"
            && wpDef.phpMyAdmin() !is null && wpDef.opcachePreset == OPcachePresetDev
            && wpDef.opcacheConfig.length == 0 && wpDef.phpExtensions.length == 0);
        assertExts(wp.phpExtensions(), ["bcmath", "curl", "dom", "exif", "fileinfo", "ftp", "gd", "iconv", "intl", "mbstring", "opcache", "openssl", "shmop", "simplexml", "sockets", "sodium", "xmlreader", "xsl", "zip", "zlib"]);
    }

    // RuntimeEnv checks for Laravel, CodeIgniter, Symfony
    {
        auto reg = newDefaultRegistry();
        auto creds = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo", user: "polka", password: "secret");
        FrameworkPlugin laravel, ci, symfony;
        reg.framework(Laravel, laravel);
        auto lEnv = laravel.runtimeEnv(RuntimeEnvContext(Environment.init, &creds));
        assert(lEnv["DB_CONNECTION"] == "mysql" && lEnv["DB_PORT"] == "3307" && lEnv["DB_DATABASE"] == "demo");

        reg.framework(CodeIgniter, ci);
        auto ciEnv = ci.runtimeEnv(RuntimeEnvContext(Environment.init, &creds));
        assert(ciEnv["database.default.hostname"] == "127.0.0.1" && ciEnv["database.default.DBDriver"] == "MySQLi");

        reg.framework(Symfony, symfony);
        auto symEnvCfg = Environment(framework: Symfony, database: new DatabaseConfig(MariaDB, "11.8"));
        auto symCreds = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo_app", user: "polka", password: "secret");
        auto sEnv = symfony.runtimeEnv(RuntimeEnvContext(symEnvCfg, &symCreds));
        assert(sEnv["DATABASE_URL"] == "mysql://polka:secret@127.0.0.1:3307/demo_app?charset=utf8mb4&serverVersion=mariadb-11.8.0");
    }

    // ------------------------------------------------------------------------
    // detect_test.go
    // ------------------------------------------------------------------------
    {
        auto reg = newDefaultRegistry();
        auto emptyDir = createTempDir("polka-detect-empty-");
        scope (exit) rmdirRecurseQuiet(emptyDir);
        foreach (pkg, want; [
            "laravel/laravel":            Laravel,
            "Laravel/Laravel:^12.0":      Laravel,
            "cakephp/app":                CakePHP,
            "drupal/recommended-project": Drupal,
            "symfony/skeleton":           Symfony,
            "codeigniter4/appstarter":    CodeIgniter,
            "roots/bedrock":              WordPress,
            "johnpbloch/wordpress":       WordPress,
            "acme/blog":                  "",
        ])
        {
            assert(reg.detectFramework(pkg, emptyDir) == want);
        }

        struct MarkerCase { string[][] markers; string want; }
        foreach (tc; [
            MarkerCase([["artisan"]], Laravel),
            MarkerCase([["bin", "cake"]], CakePHP),
            MarkerCase([["spark"]], CodeIgniter),
            MarkerCase([["wp-load.php"]], WordPress),
            MarkerCase([["web", "sites", "default", "default.settings.php"]], Drupal),
            MarkerCase([["symfony.lock"], ["bin", "console"]], Symfony),
            MarkerCase([["composer.json"]], ""),
            MarkerCase(null, ""),
        ])
        {
            auto dir = createTempDir("polka-detect-markers-");
            scope (exit) rmdirRecurseQuiet(dir);
            foreach (segs; tc.markers)
            {
                auto p = dir;
                foreach (seg; segs)
                    p = buildPath(p, seg);
                writeTestFile(p, "marker\n");
            }
            assert(reg.detectFramework("acme/blog", dir) == tc.want);
        }
    }

    // ------------------------------------------------------------------------
    // manifest_test.go
    // ------------------------------------------------------------------------
    {
        foreach (fw; [CakePHP, CodeIgniter, Drupal, WordPress, Laravel, Symfony])
        {
            auto m = loadBuiltinFrameworkManifest(fw);
            assert(m.id == fw && m.defaults.framework == fw);
        }
        auto lara = loadBuiltinFrameworkManifest(Laravel);
        assert(lara.defaults.workers["queue"].command == "php artisan queue:work");
        auto sym = loadBuiltinFrameworkManifest(Symfony);
        assert(sym.defaults.workers["messenger"].command == "php bin/console messenger:consume async");
        assert(newManifestFrameworkPlugin(Laravel).defaults().workers["queue"].command == "php artisan queue:work");

        foreach (bad; [
            ["id: demo\ndefaults:\n  docroot: public\n  workers:\n    queue:\n      replicas: 2\n", "requires a command"],
            ["id: demo\ndefaults:\n  docroot: public\n  workers:\n    \"bad name\":\n      command: php run\n", "invalid worker name"],
            ["defaults:\n  docroot: public\n", "id cannot be empty"],
            ["id: \"not a framework\"\ndefaults:\n  docroot: public\n", "invalid framework manifest id"],
            ["id: laravel\ndefaults:\n  framework: symfony\n  docroot: public\n", "defaults.framework must match id"],
            ["id: demo\ndefaults:\n  tools:\n    php: \"8.4\"\n", "defaults.docroot cannot be empty"],
        ])
        {
            bool matched = false;
            try { parseFrameworkManifest(bad[0]); }
            catch (Exception e) { matched = e.msg.canFind(bad[1]); }
            assert(matched, "expected error containing " ~ bad[1]);
        }
    }

    // ------------------------------------------------------------------------
    // manifest_declarative_test.go
    // ------------------------------------------------------------------------
    // TestManifestOnlyFramework
    {
        auto files = new MemoryFrameworkAssetSource([
            "manifests/acme.yaml": `
id: acme
defaults: {docroot: public}
database-engines: [mysql, mariadb, postgresql]
detection:
  package-prefixes: [acme/]
  markers: [acme.lock]
app-root:
  public-dir: public
  markers: [acme.lock]
values:
  driver: {by-engine: {mysql: mysql, mariadb: mysql, postgresql: pgsql}}
runtime-env:
  requires-database: true
  values:
    ACME_DRIVER: {from: values.driver}
    ACME_PASSWORD: {from: database.password}
post-composer:
  requires-database: true
  actions:
    - type: dotenv
      path: .env
      fallback: .env.example
      values:
        ACME_DRIVER: {from: runtime-env.ACME_DRIVER}
        ACME_PASSWORD: {from: runtime-env.ACME_PASSWORD}
    - type: php-array
      path: config.php
      fallback-template-file: array.php
      updates:
        - path: [Database, primary]
          values:
            driver: {from: values.driver}
            password: {from: database.password}
            url: null
    - type: php-constants
      path: constants.php
      fallback-template-file: constants.php
      insert-before: BOOTSTRAP
      values:
        DATABASE_PASSWORD: {from: database.password}
    - type: write-file
      base: docroot
      path: managed.php
      template-file: managed.php
      encoding: php-literal
      values:
        password: {from: database.password}
        driver: {from: values.driver}
    - type: php-include
      base: docroot
      path: settings.php
      fallback: settings.example.php
      include: managed.php
      marker: acme
`,
            "manifests/templates/array.php":     "<?php\r\nreturn array(\r\n  'custom' => keep_this(), // user setting\r\n);\r\n",
            "manifests/templates/constants.php": "<?php\n// user comment\ndefine('BOOTSTRAP', true);\n",
            "manifests/templates/managed.php":   "<?php\n$password = {{password}};\n$driver = {{driver}};\n",
        ]);
        auto frameworks = loadFrameworkPlugins(files);
        auto reg = newRegistry(null, frameworks);
        FrameworkPlugin plugin;
        assert(reg.framework("acme", plugin));
        auto emptyDir = createTempDir("polka-acme-empty-");
        scope (exit) rmdirRecurseQuiet(emptyDir);
        assert(reg.detectFramework("ACME/app:^2", emptyDir) == "acme");

        auto dir = createTempDir("polka-acme-test-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto app = buildPath(dir, "site");
        auto pub = buildPath(app, "public");
        mkdirRecurse(pub);
        writeTestFile(buildPath(app, "acme.lock"), "marker");
        writeTestFile(buildPath(app, ".env.example"), "APP_NAME=Acme\r\n# ACME_DRIVER=old\r\n");
        writeTestFile(buildPath(pub, "settings.example.php"), "<?php\n$custom = true;\n");
        assert(reg.detectFramework("unknown/app", app) == "acme");

        auto password = "a'\\b\"${secret}\n{{driver}}";
        auto dbCreds = DatabaseCredentials(engine: "postgresql", databaseName: "demo", user: "user", password: password);
        auto ctx = PostComposerContext(Environment(framework: "acme", docroot: "site/public"), dir, "", null, &dbCreds);
        auto rtVals = plugin.runtimeEnv(RuntimeEnvContext(ctx.environment, ctx.database));
        assert(rtVals["ACME_DRIVER"] == "pgsql" && rtVals["ACME_PASSWORD"] == password);

        plugin.postComposer(ctx);
        string[string] before;
        foreach (name, parts; [
            "site/.env":                ["APP_NAME=Acme\r\n", "ACME_DRIVER=pgsql\r\n", `\${secret}`, `\n{{driver}}`],
            "site/config.php":          ["'custom' => keep_this(), // user setting\r\n", "'driver' => 'pgsql'", "'url' => null", "'password' => " ~ phpSingleQuotedString(password)],
            "site/constants.php":       ["// user comment", "define( 'DATABASE_PASSWORD', " ~ phpSingleQuotedString(password) ~ " );", "define('BOOTSTRAP', true);"],
            "site/public/managed.php":  ["$password = " ~ phpSingleQuotedString(password) ~ ";", "$driver = 'pgsql';"],
            "site/public/settings.php": ["$custom = true;", "// <polka:acme>", "__DIR__ . '/managed.php'"],
        ])
        {
            auto data = readText(buildPath(dir, fromSlashPath(name)));
            foreach (part; parts)
                assert(data.canFind(part), name ~ " missing " ~ part);
            before[name] = data;
        }
        auto constantsText = before["site/constants.php"];
        assert(constantsText.indexOf("DATABASE_PASSWORD") < constantsText.indexOf("define('BOOTSTRAP'"));

        // Idempotency
        plugin.postComposer(ctx);
        foreach (name, original; before)
            assert(readText(buildPath(dir, fromSlashPath(name))) == original);

        // Rotation
        dbCreds.password = "rotated";
        plugin.postComposer(ctx);
        assert(readText(buildPath(app, "config.php")).canFind("'password' => 'rotated'"));

        assert(plugin.runtimeEnv(RuntimeEnvContext.init).length == 0);
        auto unguardedDir = createTempDir("polka-acme-unguarded-");
        scope (exit) rmdirRecurseQuiet(unguardedDir);
        plugin.postComposer(PostComposerContext(Environment.init, unguardedDir));
        import std.file : dirEntries, SpanMode;
        assert(dirEntries(unguardedDir, SpanMode.shallow).empty);
    }

    // TestDeclarativeManifestValidation
    {
        auto base = "id: demo\ndefaults: {docroot: public}\napp-root: {public-dir: public}\n";
        auto files = new MemoryFrameworkAssetSource(["manifests/templates/test.php": "{{missing}}"]);
        foreach (tc; [
            ["unknown field", "unknown: true", "unknown"],
            ["legacy runtime", "runtime-env: {database: {keys: standard}}", "database"],
            ["legacy composer", "post-composer: {strategy: dotenv}", "strategy"],
            ["unknown source", "values: {x: {from: database.token}}", "unknown reference"],
            ["unknown variable", "runtime-env: {values: {X: {from: values.missing}}}", "unknown reference"],
            ["cycle", "values: {a: {from: values.b}, b: {from: values.a}}", "reference cycle"],
            ["runtime cycle", "runtime-env: {values: {X: {from: runtime-env.X}}}", "reference cycle"],
            ["conflicting binding", "values: {x: {from: database.host, by-engine: {mysql: x}}}", "exactly one"],
            ["misspelled binding", "values: {x: {form: database.host}}", "form"],
            ["non scalar", "values: {x: [a, b]}", "scalar"],
            ["unknown formatter", "values: {x: {format: {kind: shell}}}", "unsupported formatter"],
            ["bad formatter fields", "values: {x: {format: {kind: string, value: x, host: x}}}", "unexpected fields"],
            ["missing URL path", "values: {x: {format: {kind: url, scheme: mysql, host: localhost}}}", "requires scheme and path"],
            ["unknown engine", "values: {x: {by-engine: {oracle: x}}}", "unsupported by-engine"],
            ["missing engine", "database-engines: [mysql, postgresql]\nvalues: {x: {by-engine: {mysql: x}}}", "missing declared"],
            ["unused branch", "values: {x: {by-engine: {mysql: x, postgresql: {from: values.missing}}}}", "unknown reference"],
            ["bad env key", "runtime-env: {values: {'BAD=KEY': literal}}", "invalid runtime"],
            ["generic marker", "detection: {markers: [composer.json]}", "not distinctive"],
            ["unsafe marker", "detection: {markers: [../marker]}", "relative path"],
            ["unsafe public dir", "app-root: {public-dir: ../public}", "relative path"],
            ["unknown action", "post-composer: {actions: [{type: shell, path: file}]}", "unsupported action"],
            ["escaped target", "post-composer: {actions: [{type: dotenv, path: ../.env, values: {X: y}}]}", "relative path"],
            ["drive target", "post-composer: {actions: [{type: dotenv, path: 'C:/file', values: {X: y}}]}", "relative path"],
            ["backslash target", "post-composer: {actions: [{type: dotenv, path: 'a\\file', values: {X: y}}]}", "relative path"],
            ["ambiguous Windows path", "post-composer: {actions: [{type: dotenv, path: '.. /file', values: {X: y}}]}", "trailing dots or spaces"],
            ["escaped fallback", "post-composer: {actions: [{type: dotenv, path: .env, fallback: ../env, values: {X: y}}]}", "relative path"],
            ["missing template", "post-composer: {actions: [{type: php-array, path: file.php, fallback-template-file: missing.php}]}", "read template"],
            ["unknown placeholder", "post-composer: {actions: [{type: write-file, path: file.php, template-file: test.php, encoding: text}]}", "unknown template placeholder"],
            ["fallback placeholder", "post-composer: {actions: [{type: php-constants, path: file.php, fallback-template-file: test.php, values: {DB: value}}]}", "without placeholders"],
            ["missing encoding", "post-composer: {actions: [{type: write-file, path: file.php, template-file: test.php}]}", "encoding"],
            ["conflicting action", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, include: file.php}]}", "require php-include"],
            ["unknown order", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, order: [Y]}]}", "unknown or duplicate"],
            ["duplicate order", "post-composer: {actions: [{type: dotenv, path: .env, values: {X: y}, order: [X, X]}]}", "unknown or duplicate"],
        ])
        {
            import std.string : replace;
            auto header = tc[1].startsWith("app-root:") ? base.replace("app-root: {public-dir: public}\n", "") : base;
            bool matched = false;
            string actualMsg;
            try
            {
                parseFrameworkManifestWithFiles(header ~ tc[1] ~ "\n", files);
            }
            catch (Exception e)
            {
                actualMsg = e.msg;
                matched = e.msg.toLower().canFind(tc[2]);
            }
            assert(matched, tc[0] ~ ": got '" ~ actualMsg ~ "', want '" ~ tc[2] ~ "'");
        }
    }

    // TestFrameworkRuntimeFormatters & TestConfiguredDatabaseVersionPrecedence
    {
        auto symfony = newManifestFrameworkPlugin(Symfony);
        foreach (tc; [
            ["mariadb", "11.8", "", "127.0.0.1", "mysql://a%40b:p%3A%2F%3F%23%25@127.0.0.1:3306/a%2Fb?charset=utf8mb4&serverVersion=mariadb-11.8.0"],
            ["mariadb", "mariadb-11.8.2", "", "127.0.0.1", "serverVersion=mariadb-11.8.2"],
            ["mariadb", "", "", "127.0.0.1", "serverVersion=mariadb-11.8.0"],
            ["mysql", "", "8.4", "127.0.0.1", "serverVersion=8.4"],
            ["mysql", "", "", "127.0.0.1", "serverVersion=mysql"],
            ["postgresql", "17", "", "::1", "postgresql://a%40b:p%3A%2F%3F%23%25@[::1]:5432/a%2Fb?charset=utf8&serverVersion=17"],
        ])
        {
            auto creds = DatabaseCredentials(engine: tc[0], host: tc[3], databaseName: "a/b", user: "a@b", password: "p:/?#%");
            auto env = Environment(database: new DatabaseConfig(tc[0], tc[1]), tools: [tc[0]: tc[2]]);
            auto vals = symfony.runtimeEnv(RuntimeEnvContext(env, &creds));
            assert(vals["DATABASE_URL"].canFind(tc[4]), vals["DATABASE_URL"]);
            assert(creds.port == 0);
        }

        foreach (tc; [
            ["11.9", "12.0", "11.8", "mariadb-11.9.0"],
            ["", "12.0", "11.8", "mariadb-12.0.0"],
            ["", "", "11.8", "mariadb-11.8.0"],
        ])
        {
            auto creds = DatabaseCredentials(engine: "mariadb", version_: tc[2]);
            auto env = Environment(database: new DatabaseConfig("mariadb", tc[0]), tools: ["mariadb": tc[1]]);
            auto vals = symfony.runtimeEnv(RuntimeEnvContext(env, &creds));
            assert(vals["DATABASE_URL"].canFind("serverVersion=" ~ tc[3]));
        }
    }

    // TestStructuredValuesWithoutDatabase & TestRuntimeBindingErrorsReturnToCaller
    {
        auto m = parseFrameworkManifest("id: static\ndefaults: {docroot: public}\nvalues:\n  empty: null\n  fallback: {from: values.empty, default: ready}\nruntime-env:\n  values:\n    STATUS: {from: values.fallback}\n    COUNT: {format: {kind: string, value: 42}}\n    FLAG: true\n");
        auto plugin = frameworkManifestToPlugin(m);
        auto vals = plugin.runtimeEnv(RuntimeEnvContext.init);
        assert(vals == ["STATUS": "ready", "COUNT": "42", "FLAG": "true"]);

        auto symfony = newManifestFrameworkPlugin(Symfony);
        auto badCreds = DatabaseCredentials(engine: "mysql", port: -1, password: "never-print-this-secret");
        bool threwRedacted = false;
        try { symfony.runtimeEnv(RuntimeEnvContext(Environment.init, &badCreds)); }
        catch (Exception e) { threwRedacted = !e.msg.canFind("never-print-this-secret"); }
        assert(threwRedacted);

        auto unguarded = frameworkManifestToPlugin(parseFrameworkManifest(
            "id: unguarded\ndefaults: {docroot: public}\nruntime-env: {values: {HOST: {from: database.host}}}\n"));
        bool threwUnguarded = false;
        try { unguarded.runtimeEnv(RuntimeEnvContext.init); } catch (Exception) { threwUnguarded = true; }
        assert(threwUnguarded);
    }

    // TestBuiltinFrameworkEngineMatrix
    {
        auto paths = [
            CakePHP: "config/app_local.php",
            CodeIgniter: ".env",
            Drupal: "web/sites/default/settings.polka.php",
            WordPress: "wp-config.php",
            Laravel: ".env",
            Symfony: ".env.local",
        ];
        foreach (plugin; defaultFrameworkPlugins())
        {
            foreach (engine; ["mysql", "mariadb", "postgresql"])
            {
                auto env = plugin.defaults();
                env.database = new DatabaseConfig(engine);
                try
                {
                    plugin.validateEnvironment(env);
                }
                catch (Exception)
                {
                    assert(plugin.id() == WordPress && engine == "postgresql");
                    continue;
                }
                auto db = DatabaseCredentials(engine: engine, databaseName: "matrix_db", user: "matrix_user", password: "matrix_password");
                auto rtVals = plugin.runtimeEnv(RuntimeEnvContext(env, &db));
                assert(rtVals.length > 0);
                auto dir = createTempDir("polka-matrix-");
                scope (exit) rmdirRecurseQuiet(dir);
                plugin.postComposer(PostComposerContext(env, dir, "", null, &db));
                auto outText = readText(buildPath(dir, fromSlashPath(paths[plugin.id()])));
                assert(outText.canFind(db.databaseName) && outText.canFind(db.user) && outText.canFind(db.password));
                if (paths[plugin.id()].endsWith(".php"))
                    scanPHP(outText);
            }
        }
    }

    // TestDetectionPriorityAndIsolation
    {
        FrameworkPlugin[] fws;
        foreach (data; [
            "id: a\ndetection: {package-prefixes: [vendor/], markers: [a.lock], priority: 1}\n",
            "id: b\ndetection: {package-substrings: [vendor], markers: [b.lock], priority: 100}\n",
            "id: c\ndetection: {package-prefixes: [vendor/], markers: [c.lock], priority: 1}\n",
        ])
        {
            fws ~= frameworkManifestToPlugin(parseFrameworkManifest(data ~ "defaults: {docroot: public}\n"));
        }
        auto reg = newRegistry(null, fws);
        auto dir = createTempDir("polka-detect-prio-");
        scope (exit) rmdirRecurseQuiet(dir);
        writeTestFile(buildPath(dir, "b.lock"), "");
        assert(reg.detectFramework("vendor/app", dir) == "a");

        auto dCopy = fws[0].detection();
        dCopy.packagePrefixes[0] = "mutated/";
        assert(fws[0].detection().packagePrefixes[0] == "vendor/");
        assert(reg.detectFramework("unknown", dir) == "b");

        bool threwStat = false;
        try { reg.detectFramework("unknown", "bad\0directory"); } catch (Exception) { threwStat = true; }
        assert(threwStat);
    }

    // ------------------------------------------------------------------------
    // framework_composer_test.go & framework_postgresql_test.go & framework_php_test.go integration tests
    // ------------------------------------------------------------------------
    // CakePHP PostgreSQL + MySQL
    {
        auto dir = createTempDir("polka-cake-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto appRoot = buildPath(dir, "cake");
        mkdirRecurse(buildPath(appRoot, "webroot"));
        writeTestFile(buildPath(appRoot, "config", "app_local.php"),
            "<?php\ndeclare(strict_types=1);\n\nreturn [\n    'debug' => true,\n    'Datasources' => [\n        'default' => [\n            'host' => 'localhost',\n        ],\n    ],\n];\n");
        FrameworkPlugin cakePHP;
        assert(newDefaultRegistry().framework(CakePHP, cakePHP));
        auto db = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo", user: "polka", password: "secret");
        cakePHP.postComposer(PostComposerContext(Environment(framework: CakePHP, docroot: "cake/webroot"), dir, "", null, &db));
        auto txt = readText(buildPath(appRoot, "config", "app_local.php"));
        foreach (exp; ["'debug' => true,", "'default' => [", "'test' => [", "'debug_kit' => [", `'className' => 'Cake\\Database\\Connection',`, `'driver' => 'Cake\\Database\\Driver\\Mysql',`, "'host' => '127.0.0.1',", "'port' => '3307',", "'username' => 'polka',", "'password' => 'secret',", "'database' => 'demo',", "'encoding' => 'utf8mb4',", "'url' => null,"])
            assert(txt.canFind(exp), "CakePHP missing " ~ exp);
        assert(!txt.canFind("Sqlite") && !txt.canFind("sqlite://"));
    }

    // CodeIgniter
    {
        auto dir = createTempDir("polka-ci-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto appRoot = buildPath(dir, "site");
        mkdirRecurse(buildPath(appRoot, "public"));
        writeTestFile(buildPath(appRoot, "env"),
            "CI_ENVIRONMENT = development\n# database.default.hostname = localhost\n# database.default.database = ci4\n# database.default.username = root\n# database.default.password = root\n# database.default.DBDriver = MySQLi\n# database.default.port = 3306\n");
        FrameworkPlugin ci;
        newDefaultRegistry().framework(CodeIgniter, ci);
        auto db = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo", user: "polka", password: "secret");
        ci.postComposer(PostComposerContext(Environment(framework: CodeIgniter, docroot: "site/public"), dir, "", null, &db));
        auto txt = readText(buildPath(appRoot, ".env"));
        foreach (exp; ["CI_ENVIRONMENT = development", "database.default.hostname=127.0.0.1", "database.default.port=3307", "database.default.database=demo", "database.default.username=polka", "database.default.password=secret", "database.default.DBDriver=MySQLi"])
            assert(txt.canFind(exp));
    }

    // Drupal + rotation after installer settings
    {
        auto dir = createTempDir("polka-drupal-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto settingsDir = buildPath(dir, "drupal", "web", "sites", "default");
        writeTestFile(buildPath(settingsDir, "default.settings.php"), "<?php\n$settings['hash_salt'] = 'sample';\n");
        FrameworkPlugin drupal;
        newDefaultRegistry().framework(Drupal, drupal);
        auto db = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo", user: "polka", password: "secret");
        auto env = Environment(framework: Drupal, docroot: "drupal/web");
        drupal.postComposer(PostComposerContext(env, dir, "", null, &db));
        auto settings = readText(buildPath(settingsDir, "settings.php"));
        assert(settings.canFind("settings.polka.php"));
        auto polkaSettings = readText(buildPath(settingsDir, "settings.polka.php"));
        foreach (exp; ["$databases['default']['default'] = [", "'database' => 'demo'", "'username' => 'polka'", "'password' => 'secret'", "'host' => '127.0.0.1'", "'port' => '3307'", "'driver' => 'mysql'"])
            assert(polkaSettings.canFind(exp));

        auto installer = "$databases['default']['default'] = ['password' => 'stale'];\n$settings['hash_salt'] = 'preserve';\n";
        write(buildPath(settingsDir, "settings.php"), settings ~ installer);
        db.password = "rotated-secret";
        drupal.postComposer(PostComposerContext(env, dir, "", null, &db));
        auto refreshed = readText(buildPath(settingsDir, "settings.php"));
        assert(refreshed.canFind(installer));
        assert(refreshed.indexOf("// <polka:settings-polka>") > refreshed.indexOf(installer));
        assert(readText(buildPath(settingsDir, "settings.polka.php")).canFind("'password' => 'rotated-secret'"));
        drupal.postComposer(PostComposerContext(env, dir, "", null, &db));
        assert(readText(buildPath(settingsDir, "settings.php")) == refreshed);
    }

    // WordPress + Laravel + Symfony
    {
        auto dir = createTempDir("polka-wls-");
        scope (exit) rmdirRecurseQuiet(dir);

        auto wpDir = buildPath(dir, "wp");
        writeTestFile(buildPath(wpDir, "wp-config-sample.php"),
            "<?php\ndefine( 'DB_NAME', 'database_name_here' );\ndefine( 'DB_USER', 'username_here' );\ndefine( 'DB_PASSWORD', 'password_here' );\ndefine( 'DB_HOST', 'localhost' );\ndefine( 'DB_CHARSET', 'utf8' );\n");
        FrameworkPlugin wp;
        newDefaultRegistry().framework(WordPress, wp);
        auto db = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo", user: "polka", password: "secret");
        wp.postComposer(PostComposerContext(Environment(framework: WordPress, docroot: "wp"), dir, "", null, &db));
        auto wpCfg = readText(buildPath(wpDir, "wp-config.php"));
        foreach (exp; ["define( 'DB_NAME', 'demo' );", "define( 'DB_USER', 'polka' );", "define( 'DB_PASSWORD', 'secret' );", "define( 'DB_HOST', '127.0.0.1:3307' );", "define( 'DB_CHARSET', 'utf8' );"])
            assert(wpCfg.canFind(exp));

        auto laraDir = buildPath(dir, "lara");
        mkdirRecurse(buildPath(laraDir, "public"));
        writeTestFile(buildPath(laraDir, ".env"), "APP_NAME=Laravel\nDB_CONNECTION=sqlite\n# DB_HOST=127.0.0.1\n# DB_PORT=3306\n");
        FrameworkPlugin lara;
        newDefaultRegistry().framework(Laravel, lara);
        lara.postComposer(PostComposerContext(Environment(framework: Laravel, docroot: "lara/public"), dir, "", null, &db));
        auto laraEnv = readText(buildPath(laraDir, ".env"));
        assert(laraEnv.canFind("DB_CONNECTION=mysql") && laraEnv.canFind("DB_HOST=127.0.0.1") && laraEnv.canFind("DB_PORT=3307"));

        auto symDir = buildPath(dir, "sym");
        writeTestFile(buildPath(symDir, "bin", "console"), "console\n");
        FrameworkPlugin sym;
        newDefaultRegistry().framework(Symfony, sym);
        auto symEnv = Environment(framework: Symfony, docroot: "sym/public", database: new DatabaseConfig(MariaDB, "11.8"));
        auto symDb = DatabaseCredentials(host: "127.0.0.1", port: 3307, databaseName: "demo_app", user: "polka", password: "secret");
        sym.postComposer(PostComposerContext(symEnv, dir, "", null, &symDb));
        assert(readText(buildPath(symDir, ".env.local")).canFind(`DATABASE_URL="mysql://polka:secret@127.0.0.1:3307/demo_app?charset=utf8mb4&serverVersion=mariadb-11.8.0"`));
    }

    // PostgreSQL runtime + Composer + compatibility checks (framework_postgresql_test.go)
    {
        auto reg = newDefaultRegistry();
        auto pgEnv = Environment(tools: [PostgreSQL: "17"], database: new DatabaseConfig(PostgreSQL, "17"));
        auto pgCreds = DatabaseCredentials(engine: PostgreSQL, version_: "17", host: "127.0.0.1", port: 5432, databaseName: "demo", user: "polka", password: "secret");
        auto ctx = RuntimeEnvContext(pgEnv, &pgCreds);

        FrameworkPlugin lara, ci, sym;
        reg.framework(Laravel, lara);
        assert(lara.runtimeEnv(ctx)["DB_CONNECTION"] == "pgsql");
        reg.framework(CodeIgniter, ci);
        assert(ci.runtimeEnv(ctx)["database.default.DBDriver"] == "Postgre");
        reg.framework(Symfony, sym);
        auto symUrl = sym.runtimeEnv(ctx)["DATABASE_URL"];
        assert(symUrl.startsWith("postgresql://") && symUrl.canFind("serverVersion=17"));

        foreach (pair; [
            [CakePHP, "config/app_local.php", `Cake\\Database\\Driver\\Postgres`, "'encoding' => 'utf8'"],
            [Drupal, "web/sites/default/settings.polka.php", "'driver' => 'pgsql'", `Drupal\\pgsql`],
        ])
        {
            FrameworkPlugin fw;
            assert(reg.framework(pair[0], fw));
            auto dir = createTempDir("polka-pg-composer-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto envCfg = pgEnv;
            envCfg.docroot = fw.defaults().docroot;
            fw.postComposer(PostComposerContext(envCfg, dir, "", null, &pgCreds));
            auto outTxt = readText(buildPath(dir, fromSlashPath(pair[1])));
            assert(outTxt.canFind(pair[2]) && outTxt.canFind(pair[3]));
        }

        Environment compatEnv;
        compatEnv.framework = Laravel;
        compatEnv.database = new DatabaseConfig(PostgreSQL, "17");
        auto toolExts = reg.toolRegistry().phpExtensions(compatEnv);
        assert(toolExts.get("pgsql", false) && toolExts.get("pdo_pgsql", false)
            && !toolExts.get("mysqli", false) && !toolExts.get("pdo_mysql", false));
        assert(!lara.phpExtensions().get("pgsql", false) && !lara.phpExtensions().get("pdo_pgsql", false));

        compatEnv.framework = WordPress;
        bool threwCompat = false;
        try { reg.validateEnvironment(compatEnv); }
        catch (Exception e) { threwCompat = e.msg.canFind("does not support database engine"); }
        assert(threwCompat);
    }

    // TestUnsafePHPFilesRemainUntouched + TestFrameworkActionPathsAndFailures + TestMultiDirectoryPublicRoot
    {
        foreach (tc; [
            [CakePHP, "config/app_local.php", "<?php return array_merge(['custom' => true], load_config());\n"],
            [CakePHP, "config/app_local.php", "<?php return ['Datasources' => load_config(), 'custom' => true];\n"],
            [CakePHP, "config/app_local.php", "<?php return ['Datasources' => [], 'Datasources' => []];\n"],
            [CakePHP, "config/app_local.php", "<?php return ['custom' => true;\n"],
            [WordPress, "wp-config.php", "<?php define($dynamic_key, 'custom');\n"],
            [WordPress, "wp-config.php", "<?php define('DB_NAME', 'a'); define('DB_NAME', 'b');\n"],
        ])
        {
            auto plugin = newManifestFrameworkPlugin(tc[0]);
            auto dir = createTempDir("polka-unsafe-php-");
            scope (exit) rmdirRecurseQuiet(dir);
            auto filename = buildPath(dir, fromSlashPath(tc[1]));
            writeTestFile(filename, tc[2]);
            auto db = DatabaseCredentials(engine: "mariadb", databaseName: "demo", user: "user", password: "secret");
            bool threw = false;
            try { plugin.postComposer(PostComposerContext(plugin.defaults(), dir, "", null, &db)); }
            catch (Exception) { threw = true; }
            assert(threw);
            assert(readText(filename) == tc[2]);
            import std.file : dirEntries, SpanMode;
            foreach (e; dirEntries(dirName(filename), ".polka-config-*", SpanMode.shallow))
                assert(false, "temporary file leaked: " ~ e.name);
        }

        // Composer target precedence
        {
            auto dir = createTempDir("polka-composer-target-");
            scope (exit) rmdirRecurseQuiet(dir);
            writeTestFile(buildPath(dir, "site", "artisan"), "marker");
            writeTestFile(buildPath(dir, "site", ".env.example"), "APP_NAME=Target\n");
            auto lara = newManifestFrameworkPlugin(Laravel);
            auto db = DatabaseCredentials(engine: "mysql", databaseName: "demo", user: "user", password: "secret");
            lara.postComposer(PostComposerContext(Environment(docroot: "public"), dir, dir, ["create-project", "laravel/laravel", "site"], &db));
            assert(readText(buildPath(dir, "site", ".env")).canFind("APP_NAME=Target\n"));
            assert(!exists(buildPath(dir, ".env")));
        }

        // Fallback read failure (directory in fallback path)
        {
            auto dir = createTempDir("polka-fallback-dir-");
            scope (exit) rmdirRecurseQuiet(dir);
            mkdirRecurse(buildPath(dir, "config", "app_local.example.php"));
            auto cake = newManifestFrameworkPlugin(CakePHP);
            auto db = DatabaseCredentials(databaseName: "demo", user: "user");
            bool threw = false;
            try { cake.postComposer(PostComposerContext(cake.defaults(), dir, "", null, &db)); }
            catch (Exception) { threw = true; }
            assert(threw);
            assert(!exists(buildPath(dir, "config", "app_local.php")));
        }

        version (Posix)
        {
            import std.file : symlink;
            // symlink escape
            {
                auto base = createTempDir("polka-sym-base-");
                auto outside = createTempDir("polka-sym-out-");
                scope (exit) { rmdirRecurseQuiet(base); rmdirRecurseQuiet(outside); }
                symlink(outside, buildPath(base, "linked"));
                bool threw = false;
                try { frameworkContainedPath(base, "linked/config.php"); }
                catch (Exception) { threw = true; }
                assert(threw);
            }
            // contained symlink preserved
            {
                auto base = createTempDir("polka-sym-keep-");
                scope (exit) rmdirRecurseQuiet(base);
                auto original = buildPath(base, "actual.env");
                writeTestFile(original, "original");
                auto link = buildPath(base, "linked.env");
                symlink(original, link);
                auto target = frameworkContainedPath(base, "linked.env");
                writeFrameworkFile(target, cast(const(ubyte)[]) "updated", octal!600);
                assert(isSymlinkPath(link));
                assert(readText(original) == "updated");
            }
        }

        // Multi-directory public root
        {
            auto project = createTempDir("polka-multidir-");
            scope (exit) rmdirRecurseQuiet(project);
            auto app = buildPath(project, "site");
            foreach (docroot; ["site/web/public", buildPath(app, "web", "public")])
            {
                auto root = frameworkAppRootFromDocroot(project, docroot, "web/public");
                assert(root == app);
                assert(frameworkDocrootPath(PostComposerContext(Environment(docroot: docroot), project), root, "web/public") == buildPath(app, "web", "public"));
            }
            assert(!frameworkPathHasSuffix("site/notpublic", "public"));
        }
    }

    // Windows read-only config replacement test (mirrors framework_replace_windows_test.go)
    version (Windows)
    {
        auto plugin = newManifestFrameworkPlugin(Drupal);
        auto dir = createTempDir("polka-win-ro-");
        scope (exit) rmdirRecurseQuiet(dir);
        auto settings = buildPath(dir, "web", "sites", "default", "settings.php");
        writeTestFile(settings, "<?php\n$settings['hash_salt'] = 'keep';\n");
        setFileMode(settings, octal!444);
        scope (exit) setFileMode(settings, octal!600);

        auto db = DatabaseCredentials(engine: "mariadb", databaseName: "demo", user: "polka", password: "secret");
        auto ctx = PostComposerContext(plugin.defaults(), dir, "", null, &db);
        foreach (_; 0 .. 2)
        {
            plugin.postComposer(ctx);
            auto data = readText(settings);
            assert(data.canFind("$settings['hash_salt'] = 'keep';"));
            assert(data.count("// <polka:settings-polka>") == 1);
            assert((getFileMode(settings) & octal!200) == 0);
        }
    }
}
