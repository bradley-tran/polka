package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// validWorkerName restricts worker names to filesystem- and log-friendly identifiers.
var validWorkerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// WorkerConfig is the YAML shape of one background worker definition under the
// top-level workers key of an environment file.
type WorkerConfig struct {
	Command  string            `yaml:"command"`
	Replicas int               `yaml:"replicas,omitempty"`
	Dir      string            `yaml:"dir,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
}

// NormalizeWorkersConfig canonicalizes worker definitions: names are lowercased
// and trimmed, commands and directories are trimmed, entries without a name or
// command are dropped, and negative replica counts are clamped to zero (zero
// means "use the default of one replica"). Returns nil when nothing remains so
// omitempty drops the workers key on save.
func NormalizeWorkersConfig(workers map[string]WorkerConfig) map[string]WorkerConfig {
	if len(workers) == 0 {
		return nil
	}

	normalized := make(map[string]WorkerConfig, len(workers))
	for name, worker := range workers {
		normalizedName := strings.ToLower(strings.TrimSpace(name))
		command := strings.TrimSpace(worker.Command)
		if normalizedName == "" || command == "" {
			continue
		}

		replicas := worker.Replicas
		if replicas < 0 {
			replicas = 0
		}
		normalized[normalizedName] = WorkerConfig{
			Command:  command,
			Replicas: replicas,
			Dir:      strings.TrimSpace(worker.Dir),
			Env:      NormalizeEnvironmentVariables(worker.Env),
		}
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// ValidateWorkersConfig reports the first problem found in the worker
// definitions: an invalid worker name, a missing or unsplittable command, or an
// invalid environment variable name.
func ValidateWorkersConfig(workers map[string]WorkerConfig) error {
	for _, name := range SortedWorkerNames(workers) {
		if !validWorkerName.MatchString(name) {
			return fmt.Errorf("invalid worker name %q: use letters, numbers, dots, dashes, or underscores", name)
		}

		worker := workers[name]
		if strings.TrimSpace(worker.Command) == "" {
			return fmt.Errorf("worker %q requires a command", name)
		}
		if _, err := SplitWorkerCommand(worker.Command); err != nil {
			return fmt.Errorf("worker %q: %w", name, err)
		}
		for key := range worker.Env {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" || strings.Contains(trimmed, "=") {
				return fmt.Errorf("worker %q has an invalid environment variable name %q", name, key)
			}
		}
	}

	return nil
}

// EffectiveWorkerReplicas resolves the replica count for a worker, defaulting
// to one when unset.
func EffectiveWorkerReplicas(worker WorkerConfig) int {
	if worker.Replicas <= 0 {
		return 1
	}

	return worker.Replicas
}

// SortedWorkerNames returns the worker names in deterministic (sorted) order.
func SortedWorkerNames(workers map[string]WorkerConfig) []string {
	if len(workers) == 0 {
		return nil
	}

	names := make([]string, 0, len(workers))
	for name := range workers {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}

// SplitWorkerCommand splits a worker command string into argv, honoring single
// and double quotes so arguments may contain spaces. It errors on unbalanced
// quotes or a command with no arguments after trimming.
func SplitWorkerCommand(command string) ([]string, error) {
	var (
		args    []string
		current strings.Builder
		quote   rune
		inArg   bool
	)
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				current.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inArg = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if inArg {
				args = append(args, current.String())
				current.Reset()
				inArg = false
			}
		default:
			current.WriteRune(r)
			inArg = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("worker command %q has an unbalanced quote", command)
	}
	if inArg {
		args = append(args, current.String())
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("worker command must not be empty")
	}

	return args, nil
}
