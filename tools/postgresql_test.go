package tools

import (
	"path/filepath"
	"strings"
	"testing"

	"polka/config"
)

func TestPostgreSQLPluginUsesPSQLDispatch(t *testing.T) {
	registry := NewDefaultRegistry()
	request, err := registry.ResolveDispatchRequest(PSQL)
	if err != nil {
		t.Fatalf("ResolveDispatchRequest(psql) error = %v", err)
	}
	if request.ConfigTool != PostgreSQL || request.Executable != PSQL {
		t.Fatalf("ResolveDispatchRequest(psql) = %#v, want postgresql/psql", request)
	}

	active := registry.ActiveCommandNames(&config.Environment{Tools: config.ToolsConfig{PostgreSQL: "17"}})
	if !containsString(active, PSQL) {
		t.Fatalf("ActiveCommandNames() = %#v, want psql", active)
	}
}

// TestPostgreSQLPSQLDispatchResolvesInstallCandidates guards against the psql
// dispatch command (which differs from the postgresql tool id) resolving to no
// candidates. It has no explicit dispatch-candidates entry and must fall back to
// the manifest's install-candidates (the psql binary).
func TestPostgreSQLPSQLDispatchResolvesInstallCandidates(t *testing.T) {
	registry := NewDefaultRegistry()
	candidates := registry.DispatchCandidates("root", PostgreSQL, PSQL, "17")
	if len(candidates) == 0 {
		t.Fatalf("DispatchCandidates(postgresql, psql) = %#v, want non-empty", candidates)
	}

	wantSuffix := filepath.Join("root", PostgreSQL, "17", "bin", "psql")
	found := false
	for _, candidate := range candidates {
		if strings.Contains(candidate, wantSuffix) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("DispatchCandidates(postgresql, psql) = %#v, want a candidate containing %q", candidates, wantSuffix)
	}
}

func TestPHPMyAdminAllowsPostgreSQL(t *testing.T) {
	environment := config.Environment{
		Tools:    config.ToolsConfig{PostgreSQL: "17", PHPMyAdmin: "5.2"},
		Database: &config.DatabaseConfig{Engine: PostgreSQL, Version: "17"},
	}
	if err := NewDefaultRegistry().ValidateEnvironment(environment); err != nil {
		t.Fatalf("ValidateEnvironment() error = %v, want PostgreSQL/phpMyAdmin warning-only pairing", err)
	}
}
