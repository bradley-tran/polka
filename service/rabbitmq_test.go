package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polka/config"
)

// TestRabbitMQDefaultsAndURLs verifies local development defaults.
func TestRabbitMQDefaultsAndURLs(t *testing.T) {
	if got := EffectiveRabbitMQPort(nil); got != 5672 {
		t.Fatalf("port = %d", got)
	}
	if got := EffectiveRabbitMQManagementPort(nil); got != 15672 {
		t.Fatalf("management port = %d", got)
	}
	if got := RabbitMQURLForConfig(nil); got != "amqp://127.0.0.1:5672" {
		t.Fatalf("url = %q", got)
	}
	if got := RabbitMQManagementURLForConfig(nil); got != "http://127.0.0.1:15672" {
		t.Fatalf("management url = %q", got)
	}
}

// TestWriteRabbitMQStateDoesNotPersistPassword protects plaintext credentials.
func TestWriteRabbitMQStateDoesNotPersistPassword(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	state := RabbitMQRuntimeState{EnvironmentName: "demo", Version: "4.3", Port: 5672, ManagementPort: 15672, Username: "polka", PasswordHash: RabbitMQPasswordHash("top-secret"), StartedAt: time.Now()}
	if err := WriteRabbitMQState(path, state); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "top-secret") {
		t.Fatal("runtime state contains plaintext password")
	}
}

// TestRabbitMQStateMatchesEnvironmentIncludesCredentialsAndPorts detects drift.
func TestRabbitMQStateMatchesEnvironmentIncludesCredentialsAndPorts(t *testing.T) {
	environment := config.Environment{Name: "demo", RabbitMQ: &config.RabbitMQConfig{Version: "4.3", Port: 5673, ManagementPort: 15673, Username: "polka", Password: "secret"}}
	state := RabbitMQRuntimeState{Version: "4.3", Port: 5673, ManagementPort: 15673, Username: "polka", PasswordHash: RabbitMQPasswordHash("secret")}
	if !RabbitMQStateMatchesEnvironment(state, environment) {
		t.Fatal("matching state rejected")
	}
	environment.RabbitMQ.Password = "changed"
	if RabbitMQStateMatchesEnvironment(state, environment) {
		t.Fatal("changed password matched")
	}
}

// TestPrepareRabbitMQRuntimeWritesPluginAndNoPasswordInConfig verifies runtime files.
func TestPrepareRabbitMQRuntimeWritesPluginAndNoPasswordInConfig(t *testing.T) {
	runDir := t.TempDir()
	spec := RabbitMQServerSpec{EnvironmentName: "demo", DataDir: filepath.Join(runDir, "data"), RunDir: filepath.Join(runDir, "run"), LogPath: filepath.Join(runDir, "rabbitmq.log"), Port: 5672, ManagementPort: 15672, Username: "polka", Password: "secret"}
	if err := prepareRabbitMQRuntime(spec); err != nil {
		t.Fatal(err)
	}
	configData, err := os.ReadFile(filepath.Join(spec.RunDir, "rabbitmq.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(configData), "secret") || !strings.Contains(string(configData), "127.0.0.1:5672") {
		t.Fatalf("rabbitmq.conf = %q", configData)
	}
	plugins, err := os.ReadFile(filepath.Join(spec.RunDir, "enabled_plugins"))
	if err != nil || !strings.Contains(string(plugins), "rabbitmq_management") {
		t.Fatalf("enabled plugins = %q, err %v", plugins, err)
	}
}
