module polka.cli.command_config_form;

// Interactive terminal settings form (`polka config` without key/value args):
// constructs ordered field specs matching `Store.configureValue`, filters out
// PIE/PECL extensions owned by `polka ext`, runs an interactive grouped TTY
// form, and applies edited changes through the backend validation path.

import core.time : Duration, dur;
import std.algorithm : canFind, sort;
import std.array : appender;
import std.conv : to;
import std.exception : collectException;
import std.format : format;
import std.string : strip, toLower;

import polka.backend :
    DatabaseConfig,
    Environment,
    FileTextWriter,
    MailpitConfig,
    MeilisearchConfig,
    PHPMyAdminConfig,
    RabbitMQConfig,
    RedisConfig,
    ServerConfig,
    Store,
    TextWriter,
    TraefikConfig,
    writeOut,
    writeOutf,
    writeOutln;
import polka.cli.env_helpers :
    resolveCommandEnvironmentName,
    writePHPCLIRuntimeWarning,
    writePHPMyAdminPostgreSQLWarning;
import polka.cli.spinner : isTTY, stdinIsTTY;
import polka.cli.types : CliStatusException, resolveStore;
import polka.config :
    OPcachePresetDev,
    OPcachePresetNone,
    OPcachePresetProduction,
    ServerTypeApache,
    ServerTypeFrankenPHP,
    ServerTypeNginx,
    ServerTypePHP;

version (Posix)
{
    import core.sys.posix.poll : poll, pollfd, POLLIN;
    import core.sys.posix.termios : ECHO, ICANON, ISIG, TCSANOW, tcgetattr, tcsetattr, termios, VMIN, VTIME;
    import core.sys.posix.unistd : read_ = read, STDIN_FILENO;
}

/// Ordered group titles displayed by the interactive settings form.
enum string configFormGroupGeneral = "General";
enum string configFormGroupPHPRuntime = "PHP runtime";
enum string configFormGroupWebServer = "Web server";
enum string configFormGroupDatabase = "Database";
enum string configFormGroupServices = "Services";
enum string configFormGroupEnvVars = "Environment variables";
enum string configFormGroupPHPExtensions = "PHP extensions";
enum string configFormGroupOPcache = "OPcache";

/// Note shown on dynamic-map sections explaining how to add brand-new entries.
enum string configFormDynamicGroupNote = "Existing entries only; add new ones with 'polka config <key> <value>'.";

/// Selects widget and serialization semantics for a form field.
enum ConfigFieldKind
{
    input = 0,
    select,
    confirm,
    port,
}

/// One editable config value in the interactive settings form.
/// `key` is the dotted path accepted by `Store.configureValue`.
final class ConfigFormField
{
    string key;
    string title;
    string desc;
    string group;
    ConfigFieldKind kind;
    string[] options; // `ConfigFieldKind.select` only; "" represents unset
    string initial;
    string value;

    this(
        string group,
        string key,
        string title,
        string desc,
        ConfigFieldKind kind,
        string initial,
        string[] options = null
    ) @safe pure
    {
        this.group = group;
        this.key = key;
        this.title = title;
        this.desc = desc;
        this.kind = kind;
        this.initial = initial;
        this.value = initial;
        this.options = options;
    }
}

/// Single key/value change produced by `configFormChanges`.
struct ConfigChange
{
    string key;
    string value;
}

ConfigFormField configInputField(string group, string key, string title, string desc, string initial) @safe pure
{
    return new ConfigFormField(group, key, title, desc, ConfigFieldKind.input, initial);
}

ConfigFormField configSelectField(string group, string key, string title, string desc, string initial, string[] options) @safe pure
{
    return new ConfigFormField(group, key, title, desc, ConfigFieldKind.select, initial, options);
}

ConfigFormField configConfirmField(string group, string key, string title, string desc, bool initial) @safe pure
{
    return new ConfigFormField(group, key, title, desc, ConfigFieldKind.confirm, initial ? "true" : "false");
}

ConfigFormField configPortField(string group, string key, string title, string desc, int port) @safe pure
{
    return new ConfigFormField(group, key, title, desc, ConfigFieldKind.port, configFormPortString(port));
}

/// Renders a configured port for editing (`0` means unset and renders empty).
string configFormPortString(int port) @safe pure
{
    if (port == 0)
        return "";
    return to!string(port);
}

/// Validates a port field (`""`, `"0"`, or `1..65535`), matching `Store.configureValue`.
void validateConfigFormPort(string value) @safe
{
    auto trimmed = value.strip();
    if (trimmed.length == 0)
        return;

    int port = 0;
    try
    {
        port = to!int(trimmed);
    }
    catch (Exception)
    {
        throw new Exception("must be an integer");
    }

    if (port != 0 && (port < 1 || port > 65535))
        throw new Exception("must be 0 or between 1 and 65535");
}

/// Builds the ordered form field specs pre-filled from `environment`.
/// Dynamic maps contribute existing entries only; PIE/PECL entries are owned by
/// `polka ext` and excluded (`environment.phpExtensions` already holds booleans only).
ConfigFormField[] buildConfigFormFields(const Environment environment) @safe
{
    ServerConfig server;
    if (environment.server !is null)
        server = *environment.server;

    DatabaseConfig database;
    if (environment.database !is null)
        database = *environment.database;

    MailpitConfig mailpit;
    if (auto v = environment.mailpit())
        mailpit = *v;

    PHPMyAdminConfig phpMyAdmin;
    if (auto v = environment.phpMyAdmin())
        phpMyAdmin = *v;

    MeilisearchConfig meilisearch;
    if (auto v = environment.meilisearch())
        meilisearch = *v;

    RedisConfig redis;
    if (auto v = environment.redis())
        redis = *v;

    RabbitMQConfig rabbitMQ;
    if (auto v = environment.rabbitMQ())
        rabbitMQ = *v;

    TraefikConfig traefik;
    if (auto v = environment.traefik())
        traefik = *v;

    ConfigFormField[] fields = [
        configInputField(configFormGroupGeneral, "framework", "Framework", "", environment.framework),
        configInputField(configFormGroupGeneral, "docroot", "Docroot", "", environment.docroot),
        configConfirmField(configFormGroupGeneral, "https", "HTTPS", "", environment.https),
        configInputField(configFormGroupGeneral, "env-file", "Env file", "", environment.envFile),
        configInputField(configFormGroupGeneral, "memory-limit", "PHP memory limit", "e.g. 256M; empty for the default", environment.memoryLimit),
        configSelectField(configFormGroupGeneral, "opcache-preset", "OPcache preset", "", environment.opcachePreset, ["", OPcachePresetNone, OPcachePresetDev, OPcachePresetProduction]),

        configInputField(configFormGroupPHPRuntime, "tools.php", "PHP version", "mutually exclusive with PHP ZTS", environment.toolVersion("php")),
        configInputField(configFormGroupPHPRuntime, "tools.php-zts", "PHP ZTS version", "mutually exclusive with PHP", environment.toolVersion("php-zts")),
        configInputField(configFormGroupPHPRuntime, "tools.frankenphp", "FrankenPHP version", "", environment.toolVersion("frankenphp")),
        configInputField(configFormGroupPHPRuntime, "tools.roadrunner", "RoadRunner version", "", environment.toolVersion("roadrunner")),
        configInputField(configFormGroupPHPRuntime, "tools.composer", "Composer version", "", environment.toolVersion("composer")),
        configInputField(configFormGroupPHPRuntime, "tools.nodejs", "Node.js version", "", environment.toolVersion("nodejs")),
        configInputField(configFormGroupPHPRuntime, "tools.mago", "Mago version", "", environment.toolVersion("mago")),

        configInputField(configFormGroupWebServer, "tools.nginx", "nginx version", "", environment.toolVersion("nginx")),
        configInputField(configFormGroupWebServer, "tools.apache", "Apache version", "", environment.toolVersion("apache")),
        configSelectField(configFormGroupWebServer, "server.type", "Server type", "", serverTypeValue(server.type), ["", ServerTypePHP, ServerTypeNginx, ServerTypeApache, ServerTypeFrankenPHP]),
        configInputField(configFormGroupWebServer, "server.hostname", "Server hostname", "", server.hostname),
        configPortField(configFormGroupWebServer, "server.port", "Server port", "", server.port),

        configInputField(configFormGroupDatabase, "tools.mysql", "MySQL version", "", environment.toolVersion("mysql")),
        configInputField(configFormGroupDatabase, "tools.mariadb", "MariaDB version", "", environment.toolVersion("mariadb")),
        configInputField(configFormGroupDatabase, "tools.postgresql", "PostgreSQL version", "", environment.toolVersion("postgresql")),
        configInputField(configFormGroupDatabase, "tools.sqlite", "SQLite version", "", environment.toolVersion("sqlite")),
        configSelectField(configFormGroupDatabase, "database.engine", "Database engine", "", database.engine, ["", "mysql", "mariadb", "postgresql"]),
        configPortField(configFormGroupDatabase, "database.port", "Database port", "", database.port),

        configInputField(configFormGroupServices, "tools.mailpit", "Mailpit version", "", mailpit.version_),
        configPortField(configFormGroupServices, "settings.mailpit.smtp-port", "Mailpit SMTP port", "", mailpit.smtpPort),
        configPortField(configFormGroupServices, "settings.mailpit.ui-port", "Mailpit UI port", "", mailpit.uiPort),
        configInputField(configFormGroupServices, "tools.phpmyadmin", "phpMyAdmin version", "", phpMyAdmin.version_),
        configPortField(configFormGroupServices, "settings.phpmyadmin.port", "phpMyAdmin port", "", phpMyAdmin.port),
        configInputField(configFormGroupServices, "tools.meilisearch", "Meilisearch version", "", meilisearch.version_),
        configPortField(configFormGroupServices, "settings.meilisearch.port", "Meilisearch port", "", meilisearch.port),
        configInputField(configFormGroupServices, "settings.meilisearch.master-key", "Meilisearch master key", "", meilisearch.masterKey),
        configInputField(configFormGroupServices, "tools.redis", "Redis version", "", redis.version_),
        configPortField(configFormGroupServices, "settings.redis.port", "Redis port", "", redis.port),
        configInputField(configFormGroupServices, "settings.redis.password", "Redis password", "", redis.password),
        configInputField(configFormGroupServices, "tools.rabbitmq", "RabbitMQ version", "Windows amd64 only", rabbitMQ.version_),
        configPortField(configFormGroupServices, "settings.rabbitmq.port", "RabbitMQ AMQP port", "", rabbitMQ.port),
        configPortField(configFormGroupServices, "settings.rabbitmq.management-port", "RabbitMQ management port", "", rabbitMQ.managementPort),
        configInputField(configFormGroupServices, "settings.rabbitmq.username", "RabbitMQ username", "defaults to guest", rabbitMQ.username),
        configInputField(configFormGroupServices, "settings.rabbitmq.password", "RabbitMQ password", "defaults to guest", rabbitMQ.password),
        configInputField(configFormGroupServices, "tools.traefik", "Traefik version", "", traefik.version_),
        configPortField(configFormGroupServices, "settings.traefik.port", "Traefik port", "", traefik.port),
    ];

    foreach (name; sortedMapKeys(environment.envVars))
        fields ~= configInputField(configFormGroupEnvVars, "env-vars." ~ name, name, "", environment.envVars[name]);
    foreach (name; sortedMapKeys(environment.phpExtensions))
        fields ~= configConfirmField(configFormGroupPHPExtensions, "php-extensions." ~ name, name, "", environment.phpExtensions[name]);
    foreach (directive; sortedMapKeys(environment.opcacheConfig))
        fields ~= configInputField(configFormGroupOPcache, "opcache-config." ~ directive, directive, "", environment.opcacheConfig[directive]);

    return fields;
}

/// Normalizes stored server type for select matching.
string serverTypeValue(string serverType) @safe pure
{
    return serverType.strip().toLower();
}

/// Returns associative-array keys sorted lexicographically for deterministic field ordering.
string[] sortedMapKeys(V)(const(V[string]) values) @safe pure
{
    auto keys = values.keys;
    keys.sort();
    return keys;
}

/// Extracts modified fields as `ConfigChange` entries in field-declaration order
/// so tool versions apply before server/database/settings validated against them.
ConfigChange[] configFormChanges(const(ConfigFormField)[] fields) @safe pure
{
    ConfigChange[] changes;
    foreach (field; fields)
    {
        if (field.value == field.initial)
            continue;
        string val = field.value;
        // Emptying a port field unsets it via "0".
        if (field.kind == ConfigFieldKind.port && val.strip().length == 0)
            val = "0";
        changes ~= ConfigChange(field.key, val);
    }
    return changes;
}

/// Applies form changes sequentially, reporting individual failures while continuing
/// independent edits. Returns the count applied and populates `applyErr` (`CliStatusException`
/// with code `1`) if any change failed.
int applyConfigFormChanges(
    TextWriter stdout,
    TextWriter stderr,
    ref Store store,
    string name,
    const(ConfigChange)[] changes,
    out Exception applyErr
) @trusted
{
    applyErr = null;
    if (changes.length == 0)
    {
        writeOutln(stdout, "No changes.");
        return 0;
    }

    int applied = 0;
    Environment* lastEnv = null;
    Environment lastEnvStorage;

    foreach (change; changes)
    {
        try
        {
            lastEnvStorage = store.configureValue(name, change.key, change.value);
            lastEnv = &lastEnvStorage;
            applied++;
            writeOutf(stdout, "Configured %s\t%s=%s\n", lastEnvStorage.name, change.key, change.value);
        }
        catch (Exception e)
        {
            writeOutf(stderr, "error: %s: %s\n", change.key, e.msg);
        }
    }

    // Emit configuration warnings once for the resulting environment state.
    if (lastEnv !is null)
    {
        writePHPCLIRuntimeWarning(stderr, *lastEnv);
        writePHPMyAdminPostgreSQLWarning(stderr, *lastEnv);
    }

    writeOutf(stdout, "Applied %d of %d change(s).\n", applied, changes.length);
    if (applied < cast(int) changes.length)
    {
        applyErr = new CliStatusException(
            1,
            format("%d of %d changes failed", cast(int) changes.length - applied, changes.length)
        );
    }
    return applied;
}

/// Opens the interactive settings form (`polka config` without arguments) in a terminal.
void runConfigForm(TextWriter stdout, TextWriter stderr, ref Store store, string name) @trusted
{
    if (!isTTY(stdout) || !stdinIsTTY())
    {
        throw new CliStatusException(
            1,
            "interactive config requires a terminal; run 'polka config <key> <value>' instead"
        );
    }

    string resolvedName;
    bool setCurrent = false;
    resolveCommandEnvironmentName(stdout, store, name, "Configuring", resolvedName, setCurrent);

    // Pre-fill from stored environment when present; otherwise start blank.
    Environment env;
    foreach (candidate; store.list())
    {
        if (candidate.name == resolvedName)
        {
            env = candidate;
            break;
        }
    }

    auto fields = buildConfigFormFields(env);
    if (!runInteractiveConfigForm(stdout, fields))
    {
        writeOutln(stdout, "Cancelled; no changes applied.");
        return;
    }

    Exception applyErr;
    auto applied = applyConfigFormChanges(stdout, stderr, store, resolvedName, configFormChanges(fields), applyErr);
    if (setCurrent && applied > 0)
        store.use(resolvedName);
    if (applyErr !is null)
        throw applyErr;
}

private struct FormGroup
{
    string title;
    string note;
    ConfigFormField[] fields;
}

private FormGroup[] groupConfigFormFields(ConfigFormField[] fields) @safe
{
    FormGroup[] groups;
    size_t[string] indexByTitle;
    foreach (field; fields)
    {
        auto idxPtr = field.group in indexByTitle;
        if (idxPtr is null)
        {
            indexByTitle[field.group] = groups.length;
            string note;
            switch (field.group)
            {
                case configFormGroupEnvVars:
                case configFormGroupPHPExtensions:
                case configFormGroupOPcache:
                    note = configFormDynamicGroupNote;
                    break;
                default:
                    break;
            }
            groups ~= FormGroup(field.group, note, [field]);
        }
        else
        {
            groups[*idxPtr].fields ~= field;
        }
    }
    return groups;
}

/// Interactive terminal renderer and input loop over grouped config fields.
/// Returns `true` when submitted or `false` when cancelled via Esc / Ctrl+C.
private bool runInteractiveConfigForm(TextWriter stdout, ConfigFormField[] fields) @trusted
{
    version (Posix)
    {
        auto groups = groupConfigFormFields(fields);
        if (groups.length == 0)
            return true;

        termios origTerm;
        if (tcgetattr(STDIN_FILENO, &origTerm) != 0)
            throw new Exception("read terminal attributes failed");

        termios rawTerm = origTerm;
        rawTerm.c_lflag &= ~(ICANON | ECHO | ISIG);
        rawTerm.c_cc[VMIN] = 1;
        rawTerm.c_cc[VTIME] = 0;
        if (tcsetattr(STDIN_FILENO, TCSANOW, &rawTerm) != 0)
            throw new Exception("enable raw terminal mode failed");

        writeOut(stdout, "\033[?1049h\033[?25l");
        flushWriter(stdout);
        scope (exit)
        {
            writeOut(stdout, "\033[?25h\033[?1049l");
            flushWriter(stdout);
            tcsetattr(STDIN_FILENO, TCSANOW, &origTerm);
        }

        size_t groupIdx = 0;
        size_t fieldIdx = 0;
        string statusMsg;

        void clampFieldIdx()
        {
            auto count = groups[groupIdx].fields.length;
            if (count == 0)
                fieldIdx = 0;
            else if (fieldIdx >= count)
                fieldIdx = count - 1;
        }

        while (true)
        {
            clampFieldIdx();
            renderInteractiveConfigGroup(stdout, groups, groupIdx, fieldIdx, statusMsg);
            statusMsg = "";

            ubyte[1] buf;
            auto n = read_(STDIN_FILENO, buf.ptr, 1);
            if (n <= 0)
                return false;

            ubyte b = buf[0];
            if (b == 0x03) // Ctrl+C
                return false;

            if (b == 0x1b) // Esc or ANSI escape sequence
            {
                auto next = readEscapeKey();
                final switch (next)
                {
                    case EscKey.cancel:
                        return false;
                    case EscKey.up:
                        if (fieldIdx > 0)
                            fieldIdx--;
                        continue;
                    case EscKey.down:
                        if (fieldIdx + 1 < groups[groupIdx].fields.length)
                            fieldIdx++;
                        continue;
                    case EscKey.left:
                        stepSelectOrConfirm(groups[groupIdx].fields[fieldIdx], -1);
                        continue;
                    case EscKey.right:
                        stepSelectOrConfirm(groups[groupIdx].fields[fieldIdx], 1);
                        continue;
                    case EscKey.shiftTab:
                        if (groupIdx > 0)
                        {
                            groupIdx--;
                            fieldIdx = 0;
                        }
                        continue;
                    case EscKey.unknown:
                        continue;
                }
            }

            auto active = groups[groupIdx].fields[fieldIdx];
            if (b == '\t') // Tab advances group (submitting on final group)
            {
                if (!validateFieldBeforeAdvance(active, statusMsg))
                    continue;
                if (groupIdx + 1 < groups.length)
                {
                    groupIdx++;
                    fieldIdx = 0;
                }
                else
                {
                    return true;
                }
                continue;
            }

            if (b == '\r' || b == '\n')
            {
                if (!validateFieldBeforeAdvance(active, statusMsg))
                    continue;
                if (fieldIdx + 1 < groups[groupIdx].fields.length)
                {
                    fieldIdx++;
                }
                else if (groupIdx + 1 < groups.length)
                {
                    groupIdx++;
                    fieldIdx = 0;
                }
                else
                {
                    return true;
                }
                continue;
            }

            if (b == 0x7f || b == 0x08) // Backspace
            {
                if ((active.kind == ConfigFieldKind.input || active.kind == ConfigFieldKind.port)
                    && active.value.length > 0)
                {
                    active.value = active.value[0 .. $ - 1];
                }
                continue;
            }

            if (b == 0x10) // Ctrl+P
            {
                if (fieldIdx > 0)
                    fieldIdx--;
                continue;
            }
            if (b == 0x0e) // Ctrl+N
            {
                if (fieldIdx + 1 < groups[groupIdx].fields.length)
                    fieldIdx++;
                continue;
            }

            if (active.kind == ConfigFieldKind.confirm && (b == ' ' || b == 'y' || b == 'Y' || b == 'n' || b == 'N'))
            {
                if (b == ' ')
                    stepSelectOrConfirm(active, 1);
                else if (b == 'y' || b == 'Y')
                    active.value = "true";
                else
                    active.value = "false";
                continue;
            }

            if (active.kind == ConfigFieldKind.select && b == ' ')
            {
                stepSelectOrConfirm(active, 1);
                continue;
            }

            if (b >= 0x20 && b < 0x7f
                && (active.kind == ConfigFieldKind.input || active.kind == ConfigFieldKind.port))
            {
                active.value ~= cast(char) b;
            }
        }
    }
    else
    {
        throw new CliStatusException(1, "interactive config form requires a POSIX terminal");
    }
}

private enum EscKey
{
    cancel,
    up,
    down,
    left,
    right,
    shiftTab,
    unknown,
}

version (Posix)
private EscKey readEscapeKey() @trusted
{
    pollfd pfd;
    pfd.fd = STDIN_FILENO;
    pfd.events = POLLIN;
    if (poll(&pfd, 1, 25) <= 0 || (pfd.revents & POLLIN) == 0)
        return EscKey.cancel;

    ubyte[2] seq;
    if (read_(STDIN_FILENO, seq.ptr, 1) <= 0)
        return EscKey.cancel;
    if (seq[0] != '[')
        return EscKey.unknown;

    if (poll(&pfd, 1, 25) <= 0 || (pfd.revents & POLLIN) == 0)
        return EscKey.unknown;
    if (read_(STDIN_FILENO, &seq[1], 1) <= 0)
        return EscKey.unknown;

    switch (seq[1])
    {
        case 'A': return EscKey.up;
        case 'B': return EscKey.down;
        case 'C': return EscKey.right;
        case 'D': return EscKey.left;
        case 'Z': return EscKey.shiftTab;
        default:  return EscKey.unknown;
    }
}

private void stepSelectOrConfirm(ConfigFormField field, int delta) @safe
{
    if (field.kind == ConfigFieldKind.confirm)
    {
        field.value = field.value == "true" ? "false" : "true";
        return;
    }
    if (field.kind == ConfigFieldKind.select && field.options.length > 0)
    {
        ptrdiff_t idx = 0;
        foreach (i, opt; field.options)
        {
            if (opt == field.value)
            {
                idx = cast(ptrdiff_t) i;
                break;
            }
        }
        auto n = cast(ptrdiff_t) field.options.length;
        idx = ((idx + delta) % n + n) % n;
        field.value = field.options[cast(size_t) idx];
    }
}

private bool validateFieldBeforeAdvance(ConfigFormField field, out string errMsg) @safe
{
    errMsg = "";
    if (field.kind == ConfigFieldKind.port)
    {
        try
        {
            validateConfigFormPort(field.value);
        }
        catch (Exception e)
        {
            errMsg = format("%s: %s", field.title, e.msg);
            return false;
        }
    }
    return true;
}

private void renderInteractiveConfigGroup(
    TextWriter stdout,
    const(FormGroup)[] groups,
    size_t groupIdx,
    size_t fieldIdx,
    string errMsg
) @trusted
{
    auto buf = appender!string();
    buf.put("\033[H\033[2J");
    buf.put(format(
        "\033[1mpolka config\033[0m  \033[2m[%d/%d] %s\033[0m\n",
        groupIdx + 1, groups.length, groups[groupIdx].title
    ));
    if (groups[groupIdx].note.length > 0)
        buf.put(format("\033[2m%s\033[0m\n", groups[groupIdx].note));
    buf.put("\n");

    foreach (i, field; groups[groupIdx].fields)
    {
        bool focused = (i == fieldIdx);
        auto marker = focused ? "\033[36m>\033[0m" : " ";
        string renderedValue;
        final switch (field.kind)
        {
            case ConfigFieldKind.input:
            case ConfigFieldKind.port:
                renderedValue = format("[%s]", field.value.length > 0 ? field.value : "");
                break;
            case ConfigFieldKind.confirm:
                renderedValue = field.value == "true" ? "[x] Yes" : "[ ] No";
                break;
            case ConfigFieldKind.select:
                auto label = field.value.length > 0 ? field.value : "(unset)";
                renderedValue = format("< %s >", label);
                break;
        }
        buf.put(format(" %s %-24s %s", marker, field.title, renderedValue));
        if (field.desc.length > 0)
            buf.put(format("  \033[2m(%s)\033[0m", field.desc));
        buf.put("\n");
    }

    buf.put("\n");
    if (errMsg.length > 0)
        buf.put(format("\033[31merror: %s\033[0m\n", errMsg));

    auto submitHint = (groupIdx + 1 < groups.length) ? "Tab next group" : "Tab/Enter submit";
    buf.put(format(
        "\033[2m↑/↓ field  ←/→/Space option  %s  Shift+Tab prev group  Esc cancel\033[0m\n",
        submitHint
    ));

    writeOut(stdout, buf.data);
    flushWriter(stdout);
}

private void flushWriter(TextWriter w) @trusted
{
    if (auto fw = cast(FileTextWriter) w)
        collectException(fw.streamFile().flush());
}

unittest
{
    import std.path : buildPath;
    import polka.backend : BufferTextWriter;
    import polka.backend : PECLExtensionConfig;
    import polka.cli.test_helpers : EnvVarGuard, readTestEnvironmentConfig;
    import polka.support.fs : createTempDir, rmdirRecurseQuiet;

    Environment testConfigFormEnvironment()
    {
        Environment env;
        env.name = "demo";
        env.framework = "laravel";
        env.tools = ["php": "8.4", "redis": "7.4"];
        env.https = true;
        env.memoryLimit = "256M";
        env.opcachePreset = "dev";
        env.envVars = ["B_VAR": "2", "A_VAR": "1"];
        env.phpExtensions = ["intl": true];
        env.pieExtensions = ["vendor/example": "^1.0"];
        env.peclExtensions = ["imagick": PECLExtensionConfig("3.7.0")];
        env.opcacheConfig = ["opcache.jit": "tracing"];
        env.server = new ServerConfig("NGINX", "demo.localhost", 8080);
        env.database = new DatabaseConfig("mysql", "", 3306);
        env.setSettingInt("redis", "port", 6379);
        env.setSettingString("redis", "password", "secret");
        return env;
    }

    ConfigFormField fieldByKey(ConfigFormField[] fields, string key)
    {
        foreach (f; fields)
        {
            if (f.key == key)
                return f;
        }
        assert(false, "buildConfigFormFields() missing field " ~ key);
    }

    ptrdiff_t fieldIndex(ConfigFormField[] fields, string key)
    {
        foreach (i, f; fields)
        {
            if (f.key == key)
                return cast(ptrdiff_t) i;
        }
        return -1;
    }

    // TestBuildConfigFormFieldsPrefillsCurrentValues
    {
        auto fields = buildConfigFormFields(testConfigFormEnvironment());
        struct Case { string key; ConfigFieldKind kind; string initial; }
        Case[] cases = [
            Case("framework", ConfigFieldKind.input, "laravel"),
            Case("tools.php", ConfigFieldKind.input, "8.4"),
            Case("https", ConfigFieldKind.confirm, "true"),
            Case("memory-limit", ConfigFieldKind.input, "256M"),
            Case("opcache-preset", ConfigFieldKind.select, "dev"),
            Case("server.type", ConfigFieldKind.select, "nginx"),
            Case("server.hostname", ConfigFieldKind.input, "demo.localhost"),
            Case("server.port", ConfigFieldKind.port, "8080"),
            Case("database.engine", ConfigFieldKind.select, "mysql"),
            Case("database.port", ConfigFieldKind.port, "3306"),
            Case("tools.redis", ConfigFieldKind.input, "7.4"),
            Case("settings.redis.port", ConfigFieldKind.port, "6379"),
            Case("settings.redis.password", ConfigFieldKind.input, "secret"),
            Case("env-vars.A_VAR", ConfigFieldKind.input, "1"),
            Case("php-extensions.intl", ConfigFieldKind.confirm, "true"),
            Case("opcache-config.opcache.jit", ConfigFieldKind.input, "tracing"),
        ];
        foreach (c; cases)
        {
            auto f = fieldByKey(fields, c.key);
            assert(f.kind == c.kind, c.key);
            assert(f.initial == c.initial, c.key ~ " got " ~ f.initial);
            assert(f.value == f.initial);
        }

        // Unset port renders empty rather than "0".
        assert(fieldByKey(fields, "settings.mailpit.smtp-port").initial == "");
        // Dynamic fields appear in sorted key order.
        auto aIdx = fieldIndex(fields, "env-vars.A_VAR");
        auto bIdx = fieldIndex(fields, "env-vars.B_VAR");
        assert(aIdx != -1 && bIdx != -1 && aIdx < bIdx);
        // PIE and PECL extensions are excluded from the form.
        foreach (excluded; ["php-extensions.vendor/example", "pecl-extensions.imagick", "tools.pie"])
            assert(fieldIndex(fields, excluded) == -1);
    }

    // TestBuildConfigFormFieldsSelectOptionsIncludeUnset
    {
        auto fields = buildConfigFormFields(Environment.init);
        assert(fieldByKey(fields, "opcache-preset").options == ["", "none", "dev", "production"]);
        assert(fieldByKey(fields, "server.type").options == ["", "php", "nginx", "apache", "frankenphp"]);
        assert(fieldByKey(fields, "database.engine").options == ["", "mysql", "mariadb", "postgresql"]);
    }

    // TestBuildConfigFormFieldsRoundTrip
    {
        auto projectDir = createTempDir("polka-form-rt-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", buildPath(projectDir, "global-cache"));
        auto store = resolveStore(buildPath(projectDir, ".polka"));

        foreach (field; buildConfigFormFields(testConfigFormEnvironment()))
        {
            string val;
            if (field.kind == ConfigFieldKind.port)
                val = "0";
            else if (field.kind == ConfigFieldKind.confirm)
                val = "true";
            store.configureValue("demo", field.key, val);
        }
    }

    // TestConfigFormChanges
    {
        auto fields = [
            configInputField("group", "tools.php", "PHP", "", "8.3"),
            configPortField("group", "server.port", "Port", "", 8080),
            configConfirmField("group", "https", "HTTPS", "", false),
            configInputField("group", "framework", "Framework", "", "laravel"),
        ];
        assert(configFormChanges(fields).length == 0);

        fields[0].value = "8.4";
        fields[1].value = "";
        fields[2].value = "true";

        auto changes = configFormChanges(fields);
        assert(changes == [
            ConfigChange("tools.php", "8.4"),
            ConfigChange("server.port", "0"),
            ConfigChange("https", "true"),
        ]);
    }

    // TestValidateConfigFormPort
    {
        import std.exception : assertNotThrown, assertThrown;
        foreach (valid; ["", " ", "0", "1", "8080", "65535"])
            assertNotThrown!Exception(validateConfigFormPort(valid));
        foreach (invalid; ["65536", "-1", "abc", "80.80"])
            assertThrown!Exception(validateConfigFormPort(invalid));
    }

    // TestApplyConfigFormChangesAppliesAllChanges
    {
        auto projectDir = createTempDir("polka-form-apply-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", buildPath(projectDir, "global-cache"));
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto store = resolveStore(buildPath(projectDir, ".polka"));

        Exception applyErr;
        auto applied = applyConfigFormChanges(outBuf, errBuf, store, "demo", [
            ConfigChange("tools.php", "8.4"),
            ConfigChange("server.port", "8080"),
        ], applyErr);
        assert(applyErr is null);
        assert(applied == 2);
        auto output = outBuf.text();
        assert(output.canFind("Configured demo\ttools.php=8.4"));
        assert(output.canFind("Configured demo\tserver.port=8080"));
        assert(output.canFind("Applied 2 of 2 change(s)."));

        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(env.toolVersion("php") == "8.4");
        assert(env.server !is null && env.server.port == 8080);
    }

    // TestApplyConfigFormChangesContinuesAfterFailure
    {
        auto projectDir = createTempDir("polka-form-partial-");
        scope (exit) rmdirRecurseQuiet(projectDir);
        auto guard = EnvVarGuard("POLKA_CACHE_DIR", buildPath(projectDir, "global-cache"));
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        auto store = resolveStore(buildPath(projectDir, ".polka"));

        Exception applyErr;
        auto applied = applyConfigFormChanges(outBuf, errBuf, store, "demo", [
            ConfigChange("tools.php", "8.4"),
            ConfigChange("bogus.key", "x"),
            ConfigChange("https", "true"),
        ], applyErr);
        assert(applied == 2);
        auto status = cast(CliStatusException) applyErr;
        assert(status !is null && status.code == 1);
        assert(errBuf.text().canFind("bogus.key"));
        assert(outBuf.text().canFind("Applied 2 of 3 change(s)."));

        auto env = readTestEnvironmentConfig(projectDir, "demo");
        assert(env.toolVersion("php") == "8.4");
        assert(env.https);
    }

    // TestApplyConfigFormChangesReportsNoChanges
    {
        auto outBuf = new BufferTextWriter();
        auto errBuf = new BufferTextWriter();
        Store emptyStore;
        Exception applyErr;
        auto applied = applyConfigFormChanges(outBuf, errBuf, emptyStore, "demo", null, applyErr);
        assert(applyErr is null && applied == 0);
        assert(outBuf.text().canFind("No changes."));
    }
}
