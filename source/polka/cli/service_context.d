module polka.cli.service_context;

// Constructs service.ServiceContext instances bridging polka.backend Store,
// active runtime environment evaluation, global TLS certificates, and stderr warnings.

import std.format : format;

import polka.backend : Environment, Store, TextWriter, writeOutf;
import polka.cli.env_runtime : resolveRuntimeEnvironment;
import polka.cli.serve_tls : ensureGlobalTLSCertificateRuntimeKey;
import polka.service : currentEnvList, ServiceContext, TLSKeyPair, toolLogRoot;
import polka.support.fs : currentOS;

/// Builds a `ServiceContext` bound to `store`, `environment`, and optional `stderr` warnings.
ServiceContext managedServiceContext(ref Store store, Environment environment, TextWriter stderr = null) @trusted
{
    // Capture a copy of store and environment value for lazy callbacks executed by service helpers.
    auto storeCopy = store;
    auto envName = environment.name;

    ServiceContext ctx;
    ctx.projectDir = store.projectDir;
    ctx.rootDir = store.rootDir;
    ctx.envsDir = store.envsDir;
    ctx.binDir = store.binDir;
    ctx.cacheDir = store.cacheDir;
    ctx.environment = environment;
    ctx.registry = store.toolRegistry();
    ctx.runtimeEnvFn = () @trusted {
        auto s = storeCopy;
        return resolveRuntimeEnvironment(currentOS(), currentEnvList(), s);
    };
    ctx.tlsCertFn = (string host) @trusted {
        auto kp = ensureGlobalTLSCertificateRuntimeKey(
            storeCopy.cacheDir,
            toolLogRoot(storeCopy.rootDir, "mailpit", envName),
            host
        );
        return TLSKeyPair(kp.certPath, kp.keyPath);
    };
    ctx.warnFn = (string msg) @trusted {
        if (stderr !is null)
            writeOutf(stderr, "warning: %s", msg);
    };
    return ctx;
}
