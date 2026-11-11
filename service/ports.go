package service

// This file implements the machine-global port lease registry. Every managed
// service a project environment runs (database, Mailpit, Redis, ...) needs a
// loopback port. When the user has not pinned one in config, the registry
// hands out a stable port from a dedicated range and records it under the
// global router directory, so several projects can run at the same time
// without colliding on the old fixed defaults.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"polka/tools"
)

// Port lease keys identify which listener of a project environment a lease
// belongs to. They are stored in the registry file, so they must stay stable.
const (
	PortKeyWeb                = "web"
	PortKeyDatabase           = "database"
	PortKeyMailpitSMTP        = "mailpit-smtp"
	PortKeyMailpitUI          = "mailpit-ui"
	PortKeyPHPMyAdmin         = "phpmyadmin"
	PortKeyMeilisearch        = "meilisearch"
	PortKeyRedis              = "redis"
	PortKeyRabbitMQ           = "rabbitmq"
	PortKeyRabbitMQManagement = "rabbitmq-management"
)

const (
	// DefaultLeasePortMin and DefaultLeasePortMax bound the leased port range.
	// The range sits below both the Linux (32768+) and Windows (49152+)
	// ephemeral ranges so outgoing connections never occupy a leased port.
	DefaultLeasePortMin = 20000
	DefaultLeasePortMax = 29999

	portRegistryFileName    = "ports.json"
	portRegistryLockDirName = "ports.lock.d"
	portRegistryVersion     = 1
)

// PortRange is an inclusive range of ports the registry may lease.
type PortRange struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// Valid reports whether the range is a usable, non-empty port range.
func (r PortRange) Valid() bool {
	return r.Min >= 1 && r.Max <= 65535 && r.Min <= r.Max
}

// DefaultPortRange returns the default leased port range.
func DefaultPortRange() PortRange {
	return PortRange{Min: DefaultLeasePortMin, Max: DefaultLeasePortMax}
}

// PortLease records one leased port for one listener of a project environment.
type PortLease struct {
	Root        string    `json:"root"`
	Environment string    `json:"environment"`
	Key         string    `json:"key"`
	Port        int       `json:"port"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// portRegistryFile is the on-disk shape of the lease registry.
type portRegistryFile struct {
	Version int         `json:"version"`
	Leases  []PortLease `json:"leases"`
}

// portAvailableFunc reports whether a loopback port can currently be bound.
// It is a variable so tests can simulate busy ports deterministically.
var portAvailableFunc = loopbackPortAvailable

// PortRegistry leases stable loopback ports from a machine-global registry
// file. The zero value is not usable; construct it with NewPortRegistry.
type PortRegistry struct {
	dir   string
	rng   PortRange
	now   func() time.Time
	avail func(int) bool
}

// NewPortRegistry returns a registry stored under dir that leases ports from
// rng. An invalid range falls back to the default range.
func NewPortRegistry(dir string, rng PortRange) PortRegistry {
	if !rng.Valid() {
		rng = DefaultPortRange()
	}

	return PortRegistry{dir: dir, rng: rng, now: time.Now, avail: portAvailableFunc}
}

// PortRegistryPath returns the lease registry file path under a router directory.
func PortRegistryPath(dir string) string {
	return filepath.Join(dir, portRegistryFileName)
}

// Lease returns the stable port leased to (root, environment, key), leasing a
// new free port when none exists yet. Reads take no lock; allocation runs
// under a cross-process directory lock so concurrent polka processes never
// lease the same port twice.
func (r PortRegistry) Lease(root, environment, key string) (int, error) {
	root = normalizeLeaseRoot(root)
	environment = normalizeLeaseEnvironment(environment)

	registry, err := r.read()
	if err != nil {
		return 0, err
	}
	if lease, ok := registry.find(root, environment, key); ok {
		return lease.Port, nil
	}

	lock, err := tools.AcquireDirLock(filepath.Join(r.dir, portRegistryLockDirName))
	if err != nil {
		return 0, fmt.Errorf("lock port registry: %w", err)
	}
	defer lock.Release()

	// Re-read under the lock: another process may have leased it meanwhile.
	registry, err = r.read()
	if err != nil {
		return 0, err
	}
	if lease, ok := registry.find(root, environment, key); ok {
		return lease.Port, nil
	}

	registry.pruneMissingRoots()
	port, err := r.pickFreePort(registry)
	if err != nil {
		return 0, err
	}
	registry.Leases = append(registry.Leases, PortLease{
		Root:        root,
		Environment: environment,
		Key:         key,
		Port:        port,
		UpdatedAt:   r.now().UTC(),
	})
	if err := r.write(registry); err != nil {
		return 0, err
	}

	return port, nil
}

// Lookup returns the port leased to (root, environment, key) without leasing
// a new one. The boolean is false when no lease exists.
func (r PortRegistry) Lookup(root, environment, key string) (int, bool, error) {
	registry, err := r.read()
	if err != nil {
		return 0, false, err
	}
	lease, ok := registry.find(normalizeLeaseRoot(root), normalizeLeaseEnvironment(environment), key)

	return lease.Port, ok, nil
}

// Leases returns every lease held by one project root, sorted by environment
// and key. An empty root returns all leases.
func (r PortRegistry) Leases(root string) ([]PortLease, error) {
	registry, err := r.read()
	if err != nil {
		return nil, err
	}
	normalized := ""
	if strings.TrimSpace(root) != "" {
		normalized = normalizeLeaseRoot(root)
	}
	leases := make([]PortLease, 0, len(registry.Leases))
	for _, lease := range registry.Leases {
		if normalized == "" || sameLeaseRoot(lease.Root, normalized) {
			leases = append(leases, lease)
		}
	}
	sort.Slice(leases, func(i, j int) bool {
		if leases[i].Root != leases[j].Root {
			return leases[i].Root < leases[j].Root
		}
		if leases[i].Environment != leases[j].Environment {
			return leases[i].Environment < leases[j].Environment
		}
		return leases[i].Key < leases[j].Key
	})

	return leases, nil
}

// pickFreePort returns the lowest port in range that is neither leased nor
// currently bound by another process.
func (r PortRegistry) pickFreePort(registry portRegistryFile) (int, error) {
	leased := make(map[int]bool, len(registry.Leases))
	for _, lease := range registry.Leases {
		leased[lease.Port] = true
	}
	for port := r.rng.Min; port <= r.rng.Max; port++ {
		if leased[port] {
			continue
		}
		if r.avail(port) {
			return port, nil
		}
	}

	return 0, fmt.Errorf("no free port left in the leased range %d-%d", r.rng.Min, r.rng.Max)
}

// read loads the registry file; a missing file is an empty registry.
func (r PortRegistry) read() (portRegistryFile, error) {
	path := PortRegistryPath(r.dir)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return portRegistryFile{Version: portRegistryVersion}, nil
	}
	if err != nil {
		return portRegistryFile{}, fmt.Errorf("read port registry %s: %w", path, err)
	}

	var registry portRegistryFile
	if err := json.Unmarshal(data, &registry); err != nil {
		return portRegistryFile{}, fmt.Errorf("decode port registry %s: %w", path, err)
	}
	if registry.Version != portRegistryVersion {
		return portRegistryFile{}, fmt.Errorf("port registry %s has unsupported version %d", path, registry.Version)
	}

	return registry, nil
}

// write persists the registry atomically so lock-free readers never observe
// a partially written file.
func (r PortRegistry) write(registry portRegistryFile) error {
	registry.Version = portRegistryVersion
	data, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode port registry: %w", err)
	}

	return writeFileAtomic(PortRegistryPath(r.dir), append(data, '\n'), 0o644)
}

// find returns the lease for (root, environment, key) when it exists.
func (registry portRegistryFile) find(root, environment, key string) (PortLease, bool) {
	for _, lease := range registry.Leases {
		if sameLeaseRoot(lease.Root, root) && lease.Environment == environment && lease.Key == key {
			return lease, true
		}
	}

	return PortLease{}, false
}

// pruneMissingRoots drops leases whose project state directory no longer
// exists, so deleted projects eventually release their ports.
func (registry *portRegistryFile) pruneMissingRoots() {
	kept := registry.Leases[:0]
	for _, lease := range registry.Leases {
		if _, err := os.Stat(lease.Root); errors.Is(err, os.ErrNotExist) {
			continue
		}
		kept = append(kept, lease)
	}
	registry.Leases = kept
}

// normalizeLeaseRoot makes lease roots comparable across invocations that
// reach the same project through different relative paths.
func normalizeLeaseRoot(root string) string {
	cleaned := filepath.Clean(strings.TrimSpace(root))
	if absolute, err := filepath.Abs(cleaned); err == nil {
		cleaned = absolute
	}

	return cleaned
}

// normalizeLeaseEnvironment maps an empty environment name to "default".
func normalizeLeaseEnvironment(environment string) string {
	trimmed := strings.TrimSpace(environment)
	if trimmed == "" {
		return "default"
	}

	return trimmed
}

// sameLeaseRoot compares roots case-insensitively on case-insensitive
// filesystems (Windows), exactly elsewhere.
func sameLeaseRoot(left, right string) bool {
	if filepath.Separator == '\\' {
		return strings.EqualFold(left, right)
	}

	return left == right
}

// loopbackPortAvailable reports whether 127.0.0.1:port can be bound now.
func loopbackPortAvailable(port int) bool {
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	_ = listener.Close()

	return true
}

// writeFileAtomic writes data to a temporary sibling file and renames it into
// place, creating the parent directory when needed.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary file for %s: %w", path, err)
	}
	tempPath := temp.Name()
	cleanup := func() { _ = os.Remove(tempPath) }
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		cleanup()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Chmod(tempPath, mode); err != nil {
		cleanup()
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		cleanup()
		return fmt.Errorf("replace %s: %w", path, err)
	}

	return nil
}

// ApplyPortLeases returns a copy of environment whose unpinned service ports
// are filled from the lease registry under routerDir. Explicitly configured
// ports are kept untouched and never leased. Only services the environment
// actually configures receive a lease, so typed views such as Mailpit() stay
// nil for unconfigured services. The input environment is never mutated, so
// callers must not persist the returned copy back to config.
func ApplyPortLeases(registry PortRegistry, rootDir string, environment Environment) (Environment, error) {
	leased := cloneEnvironmentPorts(environment)
	name := environment.Name

	lease := func(key string) (int, error) {
		return registry.Lease(rootDir, name, key)
	}
	leaseSetting := func(tool, namespace, setting, key string) error {
		if strings.TrimSpace(leased.ToolVersion(tool)) == "" || leased.SettingInt(namespace, setting) != 0 {
			return nil
		}
		port, err := lease(key)
		if err != nil {
			return fmt.Errorf("lease %s port: %w", key, err)
		}
		leased.SetSetting(namespace, setting, port)

		return nil
	}

	if leased.Database != nil && strings.TrimSpace(leased.Database.Engine) != "" && leased.Database.Port == 0 {
		port, err := lease(PortKeyDatabase)
		if err != nil {
			return environment, fmt.Errorf("lease database port: %w", err)
		}
		leased.Database.Port = port
	}
	for _, binding := range []struct{ tool, namespace, setting, key string }{
		{toolMailpit, "mailpit", "smtp-port", PortKeyMailpitSMTP},
		{toolMailpit, "mailpit", "ui-port", PortKeyMailpitUI},
		{toolPHPMyAdmin, "phpmyadmin", "port", PortKeyPHPMyAdmin},
		{toolMeilisearch, "meilisearch", "port", PortKeyMeilisearch},
		{toolRedis, "redis", "port", PortKeyRedis},
		{toolRabbitMQ, "rabbitmq", "port", PortKeyRabbitMQ},
		{toolRabbitMQ, "rabbitmq", "management-port", PortKeyRabbitMQManagement},
	} {
		if err := leaseSetting(binding.tool, binding.namespace, binding.setting, binding.key); err != nil {
			return environment, err
		}
	}

	return leased, nil
}

// cloneEnvironmentPorts copies the parts of an environment that
// ApplyPortLeases mutates (settings maps and the database config), so the
// caller's environment and its shared maps stay untouched.
func cloneEnvironmentPorts(environment Environment) Environment {
	cloned := environment
	if environment.Settings != nil {
		cloned.Settings = make(map[string]map[string]any, len(environment.Settings))
		for namespace, values := range environment.Settings {
			copied := make(map[string]any, len(values))
			for key, value := range values {
				copied[key] = value
			}
			cloned.Settings[namespace] = copied
		}
	}
	if environment.Database != nil {
		database := *environment.Database
		cloned.Database = &database
	}

	return cloned
}
