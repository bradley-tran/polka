package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunCreateIsUnknownCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"create", "demo"}); code != 2 {
		t.Fatalf("Run(create) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"create\"") {
		t.Fatalf("Run(create) stderr = %q, want unknown command message", stderr.String())
	}
}

func TestRunCurrentIsUnknownCommand(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

	if code := Run(stdout, stderr, []string{"current"}); code != 2 {
		t.Fatalf("Run(current) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command \"current\"") {
		t.Fatalf("Run(current) stderr = %q, want unknown command message", stderr.String())
	}
}
