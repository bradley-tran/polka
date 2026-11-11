module polka.tools.phpmyadmin_config;

import std.algorithm : startsWith;
import std.array : appender;
import std.base64 : Base64;
import std.conv : to;
import std.file : mkdirRecurse, write;
import std.format : format;
import std.path : absolutePath, buildPath, dirName;
import std.string : replace, representation, strip, stripRight, toLower;

import polka.config;
import polka.support.crypto : randomBytes;
import polka.support.fs : cleanPath, octal, setFileMode;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

enum string phpMyAdminConfigFileName = "config.inc.php";
enum string phpMyAdminTempDirectory = "tmp";
enum string phpMyAdminDatabaseHost = "127.0.0.1";
enum int phpMyAdminDefaultDBPort = 3306;
enum string phpMyAdminStorageDBName = "phpmyadmin";
enum string phpMyAdminManagedDBUser = "polka";

/// Single table entry mapped into phpMyAdmin's configuration storage schema.
struct PHPMyAdminStorageTable
{
    string key;
    string name;
}

/// Ordered list of phpMyAdmin configuration storage (`pmadb`) table mappings.
static immutable PHPMyAdminStorageTable[] phpMyAdminStorageTables = [
    PHPMyAdminStorageTable("bookmarktable", "pma__bookmark"),
    PHPMyAdminStorageTable("relation", "pma__relation"),
    PHPMyAdminStorageTable("table_info", "pma__table_info"),
    PHPMyAdminStorageTable("table_coords", "pma__table_coords"),
    PHPMyAdminStorageTable("pdf_pages", "pma__pdf_pages"),
    PHPMyAdminStorageTable("column_info", "pma__column_info"),
    PHPMyAdminStorageTable("history", "pma__history"),
    PHPMyAdminStorageTable("table_uiprefs", "pma__table_uiprefs"),
    PHPMyAdminStorageTable("tracking", "pma__tracking"),
    PHPMyAdminStorageTable("userconfig", "pma__userconfig"),
    PHPMyAdminStorageTable("recent", "pma__recent"),
    PHPMyAdminStorageTable("favorite", "pma__favorite"),
    PHPMyAdminStorageTable("users", "pma__users"),
    PHPMyAdminStorageTable("usergroups", "pma__usergroups"),
    PHPMyAdminStorageTable("navigationhiding", "pma__navigationhiding"),
    PHPMyAdminStorageTable("savedsearches", "pma__savedsearches"),
    PHPMyAdminStorageTable("central_columns", "pma__central_columns"),
    PHPMyAdminStorageTable("designer_settings", "pma__designer_settings"),
    PHPMyAdminStorageTable("export_templates", "pma__export_templates"),
];

/// Returns the phpMyAdmin `ToolPlugin` configured with `configureInstalledPHPMyAdmin`.
ToolPlugin phpMyAdminPlugin() @safe
{
    PluginHooks hooks;
    hooks.postInstall = (InstallContext ctx) {
        configureInstalledPHPMyAdmin(ctx);
    };
    return newManifestPlugin(PHPMyAdmin, hooks);
}

/// Generates `config.inc.php` and ensures `tmp/` exists inside the phpMyAdmin install dir.
void configureInstalledPHPMyAdmin(const InstallContext ctx) @trusted
{
    auto installDir = dirName(ctx.result.targetPath);
    if (installDir.strip().length == 0 || installDir.strip() == ".")
        installDir = buildPath(ctx.envsDir, PHPMyAdmin, ctx.result.version_);

    auto secret = generatePHPMyAdminBlowfishSecret();
    auto credentialsPath = phpMyAdminManagedDatabaseCredentialsPath(ctx.rootDir, ctx.environment.name, ctx.environment.database);
    auto configData = renderPHPMyAdminConfig(secret, ctx.environment.database, credentialsPath);

    auto configPath = buildPath(installDir, phpMyAdminConfigFileName);
    write(configPath, configData);
    setFileMode(configPath, octal!644);
    mkdirRecurse(buildPath(installDir, phpMyAdminTempDirectory));
}

/// Generates a 32-character unpadded Base64 Blowfish secret from 24 cryptographically random bytes.
string generatePHPMyAdminBlowfishSecret() @trusted
{
    auto raw = randomBytes(24);
    if (raw.length != 24)
        throw new Exception("generate phpmyadmin blowfish secret: unexpected random byte length");
    return Base64.encode(raw).idup.stripRight("=");
}

/// Resolves the runtime credentials file written before launching phpMyAdmin for MySQL/MariaDB.
string phpMyAdminManagedDatabaseCredentialsPath(
    string rootDir,
    string environmentName,
    const(DatabaseConfig)* database
) @safe
{
    if (!phpMyAdminSupportsManagedDatabase(database) || environmentName.strip().length == 0)
        return "";

    auto absRoot = cleanPath(absolutePath(rootDir));
    return buildPath(absRoot, "secrets", "db", environmentName.strip() ~ ".json");
}

/// Renders phpMyAdmin's PHP configuration file bytes.
ubyte[] renderPHPMyAdminConfig(
    string blowfishSecret,
    const(DatabaseConfig)* database,
    string credentialsPath
) @safe
{
    int port = phpMyAdminDefaultDBPort;
    bool enableStorage = false;
    if (phpMyAdminSupportsManagedDatabase(database))
    {
        enableStorage = true;
        if (database.port != 0)
            port = database.port;
    }
    bool enableManagedLogin = enableStorage && credentialsPath.strip().length > 0;

    auto builder = appender!string();
    builder.put("<?php\n");
    builder.put("declare(strict_types=1);\n\n");
    builder.put("/**\n");
    builder.put(" * Generated by Polka. Re-run polka install after editing phpMyAdmin or database settings.\n");
    builder.put(" */\n");
    builder.put("$cfg['blowfish_secret'] = ");
    builder.put(phpSingleQuotedString(blowfishSecret));
    builder.put(";\n\n");
    builder.put("$i = 0;\n");
    builder.put("$i++;\n");

    if (enableManagedLogin)
    {
        builder.put("$polkaCredentials = [];\n");
        builder.put("$polkaCredentialsPath = ");
        builder.put(phpSingleQuotedString(credentialsPath));
        builder.put(";\n");
        builder.put("if (is_readable($polkaCredentialsPath)) {\n");
        builder.put("    $polkaCredentialsData = json_decode((string) file_get_contents($polkaCredentialsPath), true);\n");
        builder.put("    if (is_array($polkaCredentialsData)) {\n");
        builder.put("        $polkaCredentials = $polkaCredentialsData;\n");
        builder.put("    }\n");
        builder.put("}\n\n");
        builder.put("$cfg['Servers'][$i]['auth_type'] = 'config';\n");
        builder.put("$cfg['Servers'][$i]['user'] = (string) ($polkaCredentials['user'] ?? ");
        builder.put(phpSingleQuotedString(phpMyAdminManagedDBUser));
        builder.put(");\n");
        builder.put("$cfg['Servers'][$i]['password'] = (string) ($polkaCredentials['password'] ?? '');\n");
    }
    else
    {
        builder.put("$cfg['Servers'][$i]['auth_type'] = 'cookie';\n");
    }

    builder.put("$cfg['Servers'][$i]['host'] = ");
    builder.put(phpSingleQuotedString(phpMyAdminDatabaseHost));
    builder.put(";\n");

    if (enableManagedLogin)
    {
        builder.put("$cfg['Servers'][$i]['port'] = (string) ($polkaCredentials['port'] ?? ");
        builder.put(phpSingleQuotedString(port.to!string));
        builder.put(");\n");
    }
    else
    {
        builder.put("$cfg['Servers'][$i]['port'] = ");
        builder.put(phpSingleQuotedString(port.to!string));
        builder.put(";\n");
    }

    builder.put("$cfg['Servers'][$i]['compress'] = false;\n");
    builder.put("$cfg['Servers'][$i]['AllowNoPassword'] = false;\n");

    if (enableStorage)
    {
        builder.put("$cfg['Servers'][$i]['pmadb'] = ");
        builder.put(phpSingleQuotedString(phpMyAdminStorageDBName));
        builder.put(";\n");
        foreach (table; phpMyAdminStorageTables)
        {
            builder.put("$cfg['Servers'][$i]['");
            builder.put(table.key);
            builder.put("'] = ");
            builder.put(phpSingleQuotedString(table.name));
            builder.put(";\n");
        }
        builder.put("\n");
    }

    builder.put("$cfg['TempDir'] = __DIR__ . '/tmp';\n");
    builder.put("$cfg['UploadDir'] = '';\n");
    builder.put("$cfg['SaveDir'] = '';\n");

    return builder.data.representation.dup;
}

/// Reports whether phpMyAdmin enables managed MySQL/MariaDB autologin & pmadb storage.
bool phpMyAdminSupportsManagedDatabase(const(DatabaseConfig)* database) @safe pure
{
    if (database is null)
        return false;
    switch (database.engine.strip().toLower())
    {
        case MySQL:
        case MariaDB:
            return true;
        default:
            return false;
    }
}

/// Escapes and wraps `value` as a single-quoted PHP string literal.
string phpSingleQuotedString(string value) @safe pure
{
    auto escaped = value.replace(`\`, `\\`).replace(`'`, `\'`);
    return "'" ~ escaped ~ "'";
}

unittest
{
    import std.algorithm : canFind;
    import std.file : exists, readText;
    import std.regex : matchFirst, regex;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    // TestConfigureInstalledPHPMyAdminWritesGeneratedConfig
    {
        auto installDir = createTempDir("polka-pma-inst-");
        scope (exit) rmdirRecurseQuiet(installDir);
        auto rootDir = createTempDir("polka-pma-root-");
        scope (exit) rmdirRecurseQuiet(rootDir);

        auto indexPath = buildPath(installDir, "index.php");
        write(indexPath, "<?php\n");

        InstallContext ctx;
        ctx.rootDir = rootDir;
        ctx.environment.name = "demo";
        ctx.environment.database = new DatabaseConfig(MariaDB, "11.8", 3307);
        ctx.result = InstallResult(PHPMyAdmin, "5.2", "", indexPath);
        configureInstalledPHPMyAdmin(ctx);

        auto configText = readText(buildPath(installDir, phpMyAdminConfigFileName));
        auto m = matchFirst(configText, regex(`\$cfg\['blowfish_secret'\] = '([^']{32})';`));
        assert(!m.empty);

        foreach (expected; [
            "$polkaCredentialsPath = " ~ phpSingleQuotedString(buildPath(rootDir, "secrets", "db", "demo.json")) ~ ";",
            "$cfg['Servers'][$i]['auth_type'] = 'config';",
            "$cfg['Servers'][$i]['user'] = (string) ($polkaCredentials['user'] ?? 'polka');",
            "$cfg['Servers'][$i]['password'] = (string) ($polkaCredentials['password'] ?? '');",
            "$cfg['Servers'][$i]['host'] = '127.0.0.1';",
            "$cfg['Servers'][$i]['port'] = (string) ($polkaCredentials['port'] ?? '3307');",
            "$cfg['Servers'][$i]['pmadb'] = 'phpmyadmin';",
            "$cfg['Servers'][$i]['relation'] = 'pma__relation';",
            "$cfg['Servers'][$i]['table_info'] = 'pma__table_info';",
            "$cfg['TempDir'] = __DIR__ . '/tmp';",
        ])
        {
            assert(configText.canFind(expected), expected);
        }
        assert(exists(buildPath(installDir, phpMyAdminTempDirectory)));
    }

    // TestRenderPHPMyAdminConfigSkipsStorageWithoutManagedDatabase
    {
        auto text = cast(string) renderPHPMyAdminConfig("abcdefghijklmnopqrstuvwxyz123456", null, "");
        assert(!text.canFind("['pmadb']"));
        assert(!text.canFind("$polkaCredentials"));
        assert(text.canFind("$cfg['Servers'][$i]['auth_type'] = 'cookie';"));
        assert(text.canFind("$cfg['Servers'][$i]['port'] = '3306';"));
    }

    // TestRenderPHPMyAdminConfigSkipsManagedDatabaseForPostgreSQL
    {
        auto rootDir = createTempDir("polka-pma-pg-");
        scope (exit) rmdirRecurseQuiet(rootDir);

        auto db = new DatabaseConfig(PostgreSQL, "17", 5432);
        auto creds = phpMyAdminManagedDatabaseCredentialsPath(rootDir, "demo", db);
        assert(creds.length == 0);

        auto text = cast(string) renderPHPMyAdminConfig("abcdefghijklmnopqrstuvwxyz123456", db, buildPath("secrets", "db", "demo.json"));
        foreach (unexpected; ["$polkaCredentials", "['pmadb']", "'5432'"])
            assert(!text.canFind(unexpected), unexpected);
        assert(text.canFind("$cfg['Servers'][$i]['auth_type'] = 'cookie';"));
        assert(text.canFind("$cfg['Servers'][$i]['port'] = '3306';"));
    }
}
