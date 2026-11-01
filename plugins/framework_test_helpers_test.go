package plugins

import "testing"

// runtimeEnvForTest keeps assertions focused on values while checking every
// runtime binding evaluation error after the hook's interface change.
func runtimeEnvForTest(t *testing.T, plugin FrameworkPlugin, ctx RuntimeEnvContext) map[string]string {
	t.Helper()
	values, err := plugin.RuntimeEnv(ctx)
	if err != nil {
		t.Fatalf("RuntimeEnv() error = %v", err)
	}
	return values
}
