package cli

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"

	"polka/backend"
)

// TestMain mocks the OS keyring and disables port leasing (and with it the
// global router) for the cli test binary, so existing tests keep their fixed
// default ports and never touch the real user cache. Leasing tests opt back in
// with useTestRouterDir.
func TestMain(m *testing.M) {
	keyring.MockInit()
	routerDirFunc = func(backend.Store) string { return "" }
	os.Exit(m.Run())
}

// useTestRouterDir enables port leasing for one test with an isolated
// temporary router directory, restoring the disabled default afterwards.
func useTestRouterDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	previous := routerDirFunc
	routerDirFunc = func(backend.Store) string { return dir }
	t.Cleanup(func() { routerDirFunc = previous })

	return dir
}
