package cli

import (
	"fmt"
	"io"
	"os"
	"runtime"

	"polka/backend"
	"polka/service"
)

func managedServiceContext(store backend.Store, environment backend.Environment, stderr io.Writer) service.Context {
	return service.Context{
		ProjectDir:  store.ProjectDir,
		RootDir:     store.RootDir,
		EnvsDir:     store.EnvsDir,
		BinDir:      store.BinDir,
		CacheDir:    store.CacheDir,
		Environment: environment,
		Registry:    store.ToolRegistry(),
		RuntimeEnv:  func() ([]string, error) { return resolveRuntimeEnvironment(runtime.GOOS, os.Environ(), store) },
		TLSCert: func(host string) (string, string, error) {
			return ensureGlobalTLSCertificateRuntimeKey(store.CacheDir, service.ToolLogRoot(store.RootDir, "mailpit", environment.Name), host)
		},
		Warnf: func(format string, args ...any) {
			if stderr != nil {
				_, _ = fmt.Fprintf(stderr, "warning: "+format, args...)
			}
		},
	}
}
