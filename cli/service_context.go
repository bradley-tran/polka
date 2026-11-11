package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"polka/backend"
	"polka/service"
)

// routerDirFunc resolves the machine-global router and port lease directory.
// An empty result disables port leasing (services fall back to their fixed
// default ports). It is a variable so the cli test binary can keep existing
// tests on fixed ports and isolate leasing tests in temporary directories.
var routerDirFunc = func(store backend.Store) string {
	return store.RouterDir()
}

// servicePortRegistry returns the lease registry for the store and whether
// leasing is enabled.
func servicePortRegistry(store backend.Store) (service.PortRegistry, bool) {
	dir := routerDirFunc(store)
	if dir == "" {
		return service.PortRegistry{}, false
	}

	// The lease range is user-configurable in router.yaml; an unreadable
	// file falls back to the default range rather than blocking services.
	leaseRange := service.DefaultPortRange()
	if settings, err := service.LoadRouterSettings(dir); err == nil {
		leaseRange = settings.LeaseRange()
	}

	return service.NewPortRegistry(dir, leaseRange), true
}

// leaseEnvironmentPorts fills the environment's unpinned service ports from
// the global lease registry. The returned copy is for runtime use only and
// must never be written back to config. With leasing disabled the
// environment is returned unchanged.
func leaseEnvironmentPorts(store backend.Store, environment backend.Environment) (backend.Environment, error) {
	registry, ok := servicePortRegistry(store)
	if !ok {
		return environment, nil
	}

	return service.ApplyPortLeases(registry, store.RootDir, environment)
}

// managedServiceContext builds the service runtime context for one
// environment, with unpinned service ports resolved from the lease registry
// and, behind the global router, service TLS turned off. A lease failure is
// reported as a warning and the fixed defaults are used.
func managedServiceContext(store backend.Store, environment backend.Environment, stderr io.Writer) service.Context {
	warnf := func(format string, args ...any) {
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "warning: "+format, args...)
		}
	}
	leased, err := runtimeServiceEnvironment(store, environment)
	if err != nil {
		warnf("could not lease service ports for environment %q, using default ports: %v\n", environment.Name, err)
		leased = environment
	}

	return service.Context{
		ProjectDir:  store.ProjectDir,
		RootDir:     store.RootDir,
		EnvsDir:     store.EnvsDir,
		BinDir:      store.BinDir,
		CacheDir:    store.CacheDir,
		Environment: leased,
		Registry:    store.ToolRegistry(),
		RuntimeEnv:  func() ([]string, error) { return resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store) },
		TLSCert: func(host string) (string, string, error) {
			return ensureGlobalTLSCertificateRuntimeKey(store.CacheDir, service.ToolLogRoot(store.RootDir, "mailpit", environment.Name), host)
		},
		Warnf: warnf,
	}
}
