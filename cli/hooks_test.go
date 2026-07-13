package cli

import (
	"reflect"
	"testing"
)

func TestDefaultCLIHookRegistryLifecycleOrder(t *testing.T) {
	registry := defaultCLIHookRegistry()

	if got, want := startServiceHookIDs(registry.startServices), []string{"database", "mailpit", "meilisearch", "redis", "rabbitmq", "traefik", "phpmyadmin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("start service hooks = %#v, want %#v", got, want)
	}
	if got, want := webserverHookIDs(registry.webservers), []string{"nginx", "apache", "frankenphp", "php"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("webserver hooks = %#v, want %#v", got, want)
	}
	if got, want := stopHookIDs(registry.stopHooks), []string{"webserver", "workers", "phpmyadmin", "traefik", "redis", "rabbitmq", "meilisearch", "database", "mailpit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stop hooks = %#v, want %#v", got, want)
	}
	if got, want := statusHookIDs(registry.runtimeStatusHooks), []string{"webserver", "workers", "phpmyadmin", "meilisearch", "redis", "rabbitmq", "traefik", "database", "mailpit"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime status hooks = %#v, want %#v", got, want)
	}
}

func TestDefaultCLIHookRegistryConfigStatusOrder(t *testing.T) {
	registry := defaultCLIHookRegistry()

	got := statusHookIDs(registry.configStatusHooks)
	want := []string{"php", "composer", "pie", "nodejs", "mago", "nginx", "apache", "frankenphp", "roadrunner", "sqlite", "meilisearch", "redis", "rabbitmq", "traefik", "phpmyadmin", "database", "mailpit", "workers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("config status hooks = %#v, want %#v", got, want)
	}
}

func startServiceHookIDs(hooks []startServiceHook) []string {
	ids := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.id)
	}

	return ids
}

func webserverHookIDs(hooks []webserverStartHook) []string {
	ids := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.id)
	}

	return ids
}

func stopHookIDs(hooks []stopHook) []string {
	ids := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.id)
	}

	return ids
}

func statusHookIDs(hooks []statusHook) []string {
	ids := make([]string, 0, len(hooks))
	for _, hook := range hooks {
		ids = append(ids, hook.id)
	}

	return ids
}
