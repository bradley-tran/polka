package service

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"polka/config"
)

// newTestPortRegistry returns a registry in a temporary directory whose
// availability probe treats every port as free unless listed in busy.
func newTestPortRegistry(t *testing.T, rng PortRange, busy ...int) PortRegistry {
	t.Helper()
	registry := NewPortRegistry(t.TempDir(), rng)
	busySet := map[int]bool{}
	for _, port := range busy {
		busySet[port] = true
	}
	registry.avail = func(port int) bool { return !busySet[port] }

	return registry
}

// TestPortRegistryLeaseIsStable verifies a lease is created once and then
// returned unchanged on later calls, including from a fresh registry value.
func TestPortRegistryLeaseIsStable(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21010})
	root := t.TempDir()

	first, err := registry.Lease(root, "default", PortKeyRedis)
	if err != nil {
		t.Fatalf("Lease() error = %v", err)
	}
	if first != 21000 {
		t.Fatalf("Lease() = %d, want first port in range 21000", first)
	}
	second, err := registry.Lease(root, "default", PortKeyRedis)
	if err != nil {
		t.Fatalf("Lease() second error = %v", err)
	}
	if second != first {
		t.Fatalf("Lease() second = %d, want stable %d", second, first)
	}

	reopened := NewPortRegistry(registry.dir, PortRange{Min: 21000, Max: 21010})
	reopened.avail = registry.avail
	third, err := reopened.Lease(root, "default", PortKeyRedis)
	if err != nil {
		t.Fatalf("Lease() reopened error = %v", err)
	}
	if third != first {
		t.Fatalf("Lease() reopened = %d, want persisted %d", third, first)
	}
}

// TestPortRegistryLeasesAreDistinct verifies different roots, environments,
// and keys never share a port.
func TestPortRegistryLeasesAreDistinct(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21010})
	rootA := t.TempDir()
	rootB := t.TempDir()

	seen := map[int]string{}
	for _, request := range []struct{ root, environment, key string }{
		{rootA, "default", PortKeyRedis},
		{rootA, "default", PortKeyDatabase},
		{rootA, "staging", PortKeyRedis},
		{rootB, "default", PortKeyRedis},
	} {
		port, err := registry.Lease(request.root, request.environment, request.key)
		if err != nil {
			t.Fatalf("Lease(%v) error = %v", request, err)
		}
		if owner, ok := seen[port]; ok {
			t.Fatalf("Lease(%v) = %d, already leased to %s", request, port, owner)
		}
		seen[port] = request.root + "/" + request.environment + "/" + request.key
	}
}

// TestPortRegistrySkipsBusyPorts verifies ports bound by other processes are
// never leased.
func TestPortRegistrySkipsBusyPorts(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21010}, 21000, 21001)

	port, err := registry.Lease(t.TempDir(), "default", PortKeyRedis)
	if err != nil {
		t.Fatalf("Lease() error = %v", err)
	}
	if port != 21002 {
		t.Fatalf("Lease() = %d, want 21002 after busy ports", port)
	}
}

// TestPortRegistryExhaustedRange verifies a full range reports an error
// instead of leasing an out-of-range port.
func TestPortRegistryExhaustedRange(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21000})
	if _, err := registry.Lease(t.TempDir(), "default", PortKeyRedis); err != nil {
		t.Fatalf("Lease() first error = %v", err)
	}
	if _, err := registry.Lease(t.TempDir(), "default", PortKeyRedis); err == nil {
		t.Fatal("Lease() on exhausted range error = nil, want error")
	}
}

// TestPortRegistryPrunesMissingRoots verifies leases of deleted projects are
// released the next time a port is allocated.
func TestPortRegistryPrunesMissingRoots(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21000})
	removed := filepath.Join(t.TempDir(), "gone")
	if err := os.MkdirAll(removed, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Lease(removed, "default", PortKeyRedis); err != nil {
		t.Fatalf("Lease() error = %v", err)
	}
	if err := os.RemoveAll(removed); err != nil {
		t.Fatal(err)
	}

	port, err := registry.Lease(t.TempDir(), "default", PortKeyRedis)
	if err != nil {
		t.Fatalf("Lease() after prune error = %v", err)
	}
	if port != 21000 {
		t.Fatalf("Lease() after prune = %d, want reclaimed 21000", port)
	}
}

// TestPortRegistryLookupDoesNotAllocate verifies Lookup is read-only.
func TestPortRegistryLookupDoesNotAllocate(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21010})
	root := t.TempDir()

	if _, ok, err := registry.Lookup(root, "default", PortKeyRedis); err != nil || ok {
		t.Fatalf("Lookup() before lease = ok %v err %v, want missing", ok, err)
	}
	if _, err := os.Stat(PortRegistryPath(registry.dir)); !os.IsNotExist(err) {
		t.Fatalf("Lookup() created the registry file: %v", err)
	}
	leased, err := registry.Lease(root, "default", PortKeyRedis)
	if err != nil {
		t.Fatal(err)
	}
	port, ok, err := registry.Lookup(root, "default", PortKeyRedis)
	if err != nil || !ok || port != leased {
		t.Fatalf("Lookup() = %d %v %v, want %d true nil", port, ok, err, leased)
	}
}

// TestPortRegistryConcurrentLeases verifies concurrent allocations through
// independent registry values (as separate processes would) never collide.
func TestPortRegistryConcurrentLeases(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	keys := []string{PortKeyWeb, PortKeyDatabase, PortKeyRedis, PortKeyMeilisearch, PortKeyMailpitSMTP, PortKeyMailpitUI, PortKeyRabbitMQ, PortKeyPHPMyAdmin}

	ports := make([]int, len(keys))
	errs := make([]error, len(keys))
	var wait sync.WaitGroup
	for index, key := range keys {
		wait.Add(1)
		go func(index int, key string) {
			defer wait.Done()
			registry := NewPortRegistry(dir, PortRange{Min: 21000, Max: 21100})
			registry.avail = func(int) bool { return true }
			ports[index], errs[index] = registry.Lease(root, "default", key)
		}(index, key)
	}
	wait.Wait()

	seen := map[int]bool{}
	for index, port := range ports {
		if errs[index] != nil {
			t.Fatalf("Lease(%s) error = %v", keys[index], errs[index])
		}
		if seen[port] {
			t.Fatalf("Lease(%s) = %d, duplicated", keys[index], port)
		}
		seen[port] = true
	}
	leases, err := NewPortRegistry(dir, DefaultPortRange()).Leases(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != len(keys) {
		t.Fatalf("Leases() = %d entries, want %d", len(leases), len(keys))
	}
}

// TestApplyPortLeasesFillsOnlyUnpinnedConfiguredServices verifies leases are
// applied to configured services without a pinned port, pinned ports win,
// unconfigured services stay unset, and the input is not mutated.
func TestApplyPortLeasesFillsOnlyUnpinnedConfiguredServices(t *testing.T) {
	registry := newTestPortRegistry(t, PortRange{Min: 21000, Max: 21100})
	root := t.TempDir()
	environment := config.Environment{
		Name:     "default",
		Tools:    config.ToolsConfig{"redis": "8.0", "mailpit": "1.30", "mariadb": "11.8"},
		Settings: config.SettingsConfig{"mailpit": {"ui-port": 8025}},
		Database: &config.DatabaseConfig{Engine: "mariadb", Version: "11.8"},
	}

	leased, err := ApplyPortLeases(registry, root, environment)
	if err != nil {
		t.Fatalf("ApplyPortLeases() error = %v", err)
	}

	if leased.Redis().Port < 21000 || leased.Redis().Port > 21100 {
		t.Fatalf("leased redis port = %d, want leased range", leased.Redis().Port)
	}
	if leased.Database.Port < 21000 {
		t.Fatalf("leased database port = %d, want leased range", leased.Database.Port)
	}
	if leased.Mailpit().UIPort != 8025 {
		t.Fatalf("leased mailpit ui port = %d, want pinned 8025", leased.Mailpit().UIPort)
	}
	if leased.Mailpit().SMTPPort < 21000 {
		t.Fatalf("leased mailpit smtp port = %d, want leased range", leased.Mailpit().SMTPPort)
	}
	if leased.Meilisearch() != nil || leased.RabbitMQ() != nil || leased.PHPMyAdmin() != nil {
		t.Fatal("ApplyPortLeases() configured services the environment does not define")
	}
	if environment.Database.Port != 0 || environment.SettingInt("redis", "port") != 0 || environment.SettingInt("mailpit", "smtp-port") != 0 {
		t.Fatal("ApplyPortLeases() mutated its input environment")
	}

	again, err := ApplyPortLeases(registry, root, environment)
	if err != nil {
		t.Fatal(err)
	}
	if again.Redis().Port != leased.Redis().Port || again.Database.Port != leased.Database.Port {
		t.Fatal("ApplyPortLeases() is not stable across calls")
	}
}

// TestPortRangeValidity covers range validation and the default fallback.
func TestPortRangeValidity(t *testing.T) {
	if !DefaultPortRange().Valid() {
		t.Fatal("DefaultPortRange() is invalid")
	}
	for _, invalid := range []PortRange{{}, {Min: 10, Max: 5}, {Min: 1, Max: 70000}} {
		if invalid.Valid() {
			t.Fatalf("PortRange %+v Valid() = true, want false", invalid)
		}
	}
	if registry := NewPortRegistry(t.TempDir(), PortRange{}); registry.rng != DefaultPortRange() {
		t.Fatalf("NewPortRegistry(invalid range) range = %+v, want default", registry.rng)
	}
}

// TestEnsureAddressesFree verifies a busy address blocks a service start with
// a message naming the address.
func TestEnsureAddressesFree(t *testing.T) {
	busy := func(address string) bool { return address == "127.0.0.1:21001" }
	if err := ensureAddressesFree("redis", busy, "127.0.0.1:21000"); err != nil {
		t.Fatalf("ensureAddressesFree(free) error = %v", err)
	}
	err := ensureAddressesFree("redis", busy, "127.0.0.1:21000", "127.0.0.1:21001")
	if err == nil || err.Error() != "redis cannot start because 127.0.0.1:21001 is already in use" {
		t.Fatalf("ensureAddressesFree(busy) error = %v, want in-use error", err)
	}
}
