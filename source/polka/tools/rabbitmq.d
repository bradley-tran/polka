module polka.tools.rabbitmq;

import std.algorithm : canFind;

import polka.config;
import polka.tools.ids;
import polka.tools.manifest;
import polka.tools.types;

/// Returns the Erlang OTP `ToolPlugin` declared via `tools/manifests/erlang.yaml`.
ToolPlugin erlangPlugin() @safe
{
    return newManifestPlugin(Erlang);
}

/// Returns the RabbitMQ broker `ToolPlugin` with cross-field validation.
ToolPlugin rabbitMQPlugin() @safe
{
    PluginHooks hooks;
    hooks.validate = (const Environment environment) {
        validateRabbitMQConfig(environment.rabbitMQ());
    };
    return newManifestPlugin(RabbitMQ, hooks);
}

/// Enforces broker-specific cross-field rules beyond manifest constraint & port-range checks.
void validateRabbitMQConfig(const(RabbitMQConfig)* rabbitMQ) @safe pure
{
    if (rabbitMQ is null)
        return;

    int port = rabbitMQ.port;
    if (port == 0)
        port = 5672;
    int managementPort = rabbitMQ.managementPort;
    if (managementPort == 0)
        managementPort = 15672;
    if (port == managementPort)
        throw new Exception("rabbitmq port and management port must be different");
    if (rabbitMQ.username.canFind('\r') || rabbitMQ.username.canFind('\n')
        || rabbitMQ.password.canFind('\r') || rabbitMQ.password.canFind('\n'))
    {
        throw new Exception("rabbitmq credentials must be single-line values");
    }
}

unittest
{
    import std.algorithm : canFind;
    import polka.support.yaml : YamlValue;
    import polka.tools.download_io : resolveManifestDownloadAsset;

    // TestValidateRabbitMQConfig
    auto plugin = rabbitMQPlugin();
    struct Case { string name; Environment env; string want; }

    Environment samePorts;
    samePorts.tools = [RabbitMQ: "4.3"];
    samePorts.settings = [RabbitMQ: ["port": YamlValue.fromInt(5672), "management-port": YamlValue.fromInt(5672)]];

    Environment validEnv;
    validEnv.tools = [RabbitMQ: "4.3"];

    Environment badMajor;
    badMajor.tools = [RabbitMQ: "3.13"];

    Environment userOnly;
    userOnly.tools = [RabbitMQ: "4.3"];
    userOnly.settings = [RabbitMQ: ["username": YamlValue.fromString("demo")]];

    Environment multilineCred;
    multilineCred.tools = [RabbitMQ: "4.3"];
    multilineCred.settings = [RabbitMQ: ["username": YamlValue.fromString("demo\ninjected")]];

    Environment noVer;
    noVer.settings = [RabbitMQ: ["port": YamlValue.fromInt(5672)]];

    foreach (tc; [
        Case("valid", validEnv, ""),
        Case("same ports", samePorts, "must be different"),
        Case("unsupported major", badMajor, "requires >= 4.0.4 and < 5"),
        Case("username with default password", userOnly, ""),
        Case("multiline credentials", multilineCred, "single-line"),
        Case("settings without version", noVer, "rabbitmq configuration requires version"),
    ])
    {
        string errMsg = "";
        try { plugin.validate(tc.env); }
        catch (Exception e) { errMsg = e.msg; }

        if (tc.want.length == 0)
            assert(errMsg.length == 0, tc.name ~ ": " ~ errMsg);
        else
            assert(errMsg.canFind(tc.want), tc.name ~ ": got " ~ errMsg);
    }

    // TestRabbitMQWindowsAssets
    auto m = loadBuiltinManifest(RabbitMQ);
    auto asset = resolveManifestDownloadAsset(RabbitMQ, m.download.assets, "4.3", "4.3.2", "v4.3.2", "windows", "amd64", null);
    assert(asset.fileName == "rabbitmq-server-windows-4.3.2.zip");
    assert(asset.archiveFormat == ArchiveFormat.zip);

    bool linuxThrew = false;
    try { resolveManifestDownloadAsset(RabbitMQ, m.download.assets, "4.3", "4.3.2", "v4.3.2", "linux", "amd64", null); }
    catch (Exception) { linuxThrew = true; }
    assert(linuxThrew);
}
