package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLinuxTrustStoreIntegration installs and rotates real CAs only inside
// disposable Docker containers. It is opt-in because it downloads distro images
// and packages; the host trust store is never mounted or modified.
//
//	POLKA_CERT_TRUST_INTEGRATION=1 go test ./cli -run '^TestLinuxTrustStoreIntegration$' -v -timeout 20m
func TestLinuxTrustStoreIntegration(t *testing.T) {
	if os.Getenv("POLKA_CERT_TRUST_INTEGRATION") != "1" {
		t.Skip("set POLKA_CERT_TRUST_INTEGRATION=1 to test native CA tools in Docker")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("Docker is required for certificate trust-store integration: %v", err)
	}
	artifacts := t.TempDir()
	binary := filepath.Join(artifacts, "polka")
	build := exec.Command("go", "build", "-o", binary, "..")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build integration CLI: %v\n%s", err, output)
	}

	for _, distro := range []struct {
		name    string
		image   string
		setup   string
		anchors string
		sudo    string
	}{
		{
			name: "debian", image: "debian:bookworm-slim",
			setup:   "apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates openssl sudo passwd util-linux",
			anchors: "/usr/local/share/ca-certificates",
			sudo: `
useradd -m polka-smoke
printf 'polka-smoke ALL=(root) NOPASSWD: ALL\n' > /etc/sudoers.d/polka-smoke
chmod 0440 /etc/sudoers.d/polka-smoke
runuser -u polka-smoke -- env POLKA_CACHE_DIR=/tmp/polka-user-cache polka cert-install --no-encryption
openssl verify /tmp/polka-user-cache/polka/cert/polka-local.crt
test "$(stat -c %U /tmp/polka-user-cache/polka/cert/polka-local-ca.key)" = polka-smoke
runuser -u polka-smoke -- env POLKA_CACHE_DIR=/tmp/polka-user-cache polka cert-install --no-encryption
openssl verify /tmp/polka-user-cache/polka/cert/polka-local.crt
openssl verify /tmp/polka-cache/polka/cert/polka-local.crt
cp /tmp/polka-user-cache/polka/cert/polka-local-ca.crt /tmp/user-before.crt
printf 'polka-smoke ALL=(root) PASSWD: ALL\n' > /etc/sudoers.d/polka-smoke
if runuser -u polka-smoke -- env POLKA_CACHE_DIR=/tmp/polka-user-cache polka cert-install --no-encryption; then
  echo 'noninteractive sudo unexpectedly authenticated' >&2
  exit 1
fi
cmp /tmp/user-before.crt /tmp/polka-user-cache/polka/cert/polka-local-ca.crt
openssl verify /tmp/polka-user-cache/polka/cert/polka-local.crt
`,
		},
		{
			name: "fedora", image: "fedora:latest",
			setup:   "if ! command -v update-ca-trust >/dev/null || ! command -v openssl >/dev/null; then dnf install -y ca-certificates openssl coreutils; fi",
			anchors: "/etc/pki/ca-trust/source/anchors",
		},
		{
			name: "arch", image: "archlinux:base",
			setup:   "if ! command -v update-ca-trust >/dev/null || ! command -v openssl >/dev/null; then pacman -Syu --noconfirm --needed ca-certificates-utils ca-certificates-mozilla openssl; fi",
			anchors: "/etc/ca-certificates/trust-source/anchors",
		},
		{
			name: "opensuse", image: "opensuse/tumbleweed:latest",
			setup:   "if ! command -v update-ca-certificates >/dev/null || ! command -v openssl >/dev/null; then zypper --non-interactive install --no-recommends ca-certificates openssl coreutils; fi",
			anchors: "/etc/pki/trust/anchors",
		},
		{
			name: "alpine", image: "alpine:latest",
			setup:   "apk add --no-cache ca-certificates openssl",
			anchors: "/usr/local/share/ca-certificates",
		},
	} {
		t.Run(distro.name, func(t *testing.T) {
			t.Parallel()
			containerID := sha256.Sum256([]byte(t.TempDir()))
			containerName := fmt.Sprintf("polka-trust-%x", containerID[:8])
			t.Cleanup(func() {
				cleanupContext, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				output, err := exec.CommandContext(cleanupContext, "docker", "rm", "--force", containerName).CombinedOutput()
				if err != nil && !strings.Contains(string(output), "No such container") {
					t.Errorf("remove integration container: %v\n%s", err, output)
				}
			})
			// Verify through OpenSSL's default system store, including rejection of
			// each retired CA and its server certificate after two rotations.
			script := `set -eu
` + distro.setup + `
cp /polka-test/polka /usr/local/bin/polka
export POLKA_CACHE_DIR=/tmp/polka-cache
polka cert-install --no-encryption
tls=/tmp/polka-cache/polka/cert
openssl verify "$tls/polka-local.crt"
for rotation in 1 2; do
  cp "$tls/polka-local-ca.crt" /tmp/retired-ca.crt
  cp "$tls/polka-local.crt" /tmp/retired-server.crt
  polka cert-install --no-encryption
  openssl verify "$tls/polka-local.crt"
  if openssl verify /tmp/retired-ca.crt >/dev/null 2>&1; then
    echo 'retired CA is still trusted' >&2
    exit 1
  fi
  if openssl verify /tmp/retired-server.crt >/dev/null 2>&1; then
    echo 'retired server certificate is still trusted' >&2
    exit 1
  fi
done
set -- ` + distro.anchors + `/polka-local-ca-*.crt
test "$#" -eq 1
test -f "$1"
case "$(cat "$1")" in
  *'PRIVATE KEY'*) echo 'private key copied into trust store' >&2; exit 1 ;;
esac
` + distro.sudo + `
echo 'trust-store installation and rotation passed'
`
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, "docker", "run", "--rm", "--name", containerName,
				"--mount", "type=bind,src="+artifacts+",dst=/polka-test,readonly",
				"--entrypoint", "/bin/sh", distro.image, "-c", script)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("%s native trust-store smoke test: %v\n%s", distro.name, err, output)
			}
			t.Logf("%s", output[max(0, len(output)-2000):])
		})
	}
}
