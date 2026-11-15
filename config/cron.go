package config

import (
	"fmt"
	"sort"
	"strings"
)

// CronJobConfig is the YAML shape of one scheduled job under the top-level
// cron key of an environment file. Polka's own cron runner executes Command
// whenever Schedule matches, with the same environment as `polka exec`.
type CronJobConfig struct {
	Schedule string            `yaml:"schedule"`
	Command  string            `yaml:"command"`
	Dir      string            `yaml:"dir,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
}

// NormalizeCronConfig canonicalizes cron job definitions: names are lowercased
// and trimmed, schedules have runs of whitespace collapsed, commands and
// directories are trimmed, and entries without a name or command are dropped.
// Returns nil when nothing remains so omitempty drops the cron key on save.
func NormalizeCronConfig(jobs map[string]CronJobConfig) map[string]CronJobConfig {
	if len(jobs) == 0 {
		return nil
	}

	normalized := make(map[string]CronJobConfig, len(jobs))
	for name, job := range jobs {
		normalizedName := strings.ToLower(strings.TrimSpace(name))
		command := strings.TrimSpace(job.Command)
		if normalizedName == "" || command == "" {
			continue
		}

		normalized[normalizedName] = CronJobConfig{
			Schedule: strings.Join(strings.Fields(job.Schedule), " "),
			Command:  command,
			Dir:      strings.TrimSpace(job.Dir),
			Env:      NormalizeEnvironmentVariables(job.Env),
		}
	}
	if len(normalized) == 0 {
		return nil
	}

	return normalized
}

// ValidateCronConfig reports the first problem found in the cron job
// definitions: an invalid job name, a missing or unparsable schedule, a
// missing or unsplittable command, or an invalid environment variable name.
func ValidateCronConfig(jobs map[string]CronJobConfig) error {
	for _, name := range SortedCronJobNames(jobs) {
		if !validWorkerName.MatchString(name) {
			return fmt.Errorf("invalid cron job name %q: use letters, numbers, dots, dashes, or underscores", name)
		}

		job := jobs[name]
		if strings.TrimSpace(job.Schedule) == "" {
			return fmt.Errorf("cron job %q requires a schedule", name)
		}
		if _, err := ParseCronSchedule(job.Schedule); err != nil {
			return fmt.Errorf("cron job %q: %w", name, err)
		}
		if strings.TrimSpace(job.Command) == "" {
			return fmt.Errorf("cron job %q requires a command", name)
		}
		if _, err := SplitWorkerCommand(job.Command); err != nil {
			return fmt.Errorf("cron job %q: %w", name, err)
		}
		for key := range job.Env {
			trimmed := strings.TrimSpace(key)
			if trimmed == "" || strings.Contains(trimmed, "=") {
				return fmt.Errorf("cron job %q has an invalid environment variable name %q", name, key)
			}
		}
	}

	return nil
}

// SortedCronJobNames returns the cron job names in deterministic (sorted) order.
func SortedCronJobNames(jobs map[string]CronJobConfig) []string {
	if len(jobs) == 0 {
		return nil
	}

	names := make([]string, 0, len(jobs))
	for name := range jobs {
		names = append(names, name)
	}
	sort.Strings(names)

	return names
}
