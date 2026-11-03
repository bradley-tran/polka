package cli

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/term"
)

// linuxTrustStoreSystem makes native operations replaceable without accessing
// the host trust store or authenticating sudo in unit tests.
type linuxTrustStoreSystem struct {
	readFile    func(string) ([]byte, error)
	stat        func(string) (os.FileInfo, error)
	lstat       func(string) (os.FileInfo, error)
	commandPath func(string) (string, error)
	euid        func() int
	interactive func() bool
	run         func(string, []string, io.Writer) error
}

// linuxTrustStoreBackend describes a distribution's administrator-managed CA
// directory and the commands that rebuild its application-facing stores.
type linuxTrustStoreBackend struct {
	anchorDir      string
	layoutDir      string
	refreshCommand string
	refreshArgs    []string
	removeArgs     []string
	packageName    string
}

// linuxTrustStore retains the same backend and resolved executables throughout
// rotation. Only its native file and refresh commands are elevated.
type linuxTrustStore struct {
	system      linuxTrustStoreSystem
	backend     linuxTrustStoreBackend
	installPath string
	removePath  string
	refreshPath string
	sudoPath    string
}

// defaultLinuxTrustStoreSystem uses real filesystem and process operations.
// Helpers remain portable so distro and failure tests also run on Windows.
func defaultLinuxTrustStoreSystem() linuxTrustStoreSystem {
	return linuxTrustStoreSystem{
		readFile: os.ReadFile, stat: os.Stat, lstat: os.Lstat,
		commandPath: linuxTrustStoreCommandPath,
		euid:        os.Geteuid,
		interactive: func() bool { return term.IsTerminal(os.Stdin.Fd()) },
		run:         runLinuxTrustStoreCommand,
	}
}

// prepareLinuxTrustStore checks prerequisites and authenticates before the
// caller removes a trusted CA or replaces any cached certificate material.
func prepareLinuxTrustStore(system linuxTrustStoreSystem, stderr io.Writer) (*linuxTrustStore, error) {
	release, err := system.readFile("/etc/os-release")
	if os.IsNotExist(err) {
		release, err = system.readFile("/usr/lib/os-release")
	}
	if err != nil {
		return nil, fmt.Errorf("read Linux distribution information: %w", err)
	}
	backend, err := detectLinuxTrustStore(release)
	if err != nil {
		return nil, err
	}
	info, err := system.stat(backend.layoutDir)
	if err != nil {
		return nil, fmt.Errorf("inspect Linux trust-store directory %s: %w; install %s and retry", backend.layoutDir, err, backend.packageName)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("Linux trust-store path %s is not a directory; repair %s and retry", backend.layoutDir, backend.packageName)
	}

	store := &linuxTrustStore{system: system, backend: backend}
	for _, tool := range []struct {
		name string
		path *string
	}{
		{backend.refreshCommand, &store.refreshPath},
		{"install", &store.installPath},
		{"rm", &store.removePath},
	} {
		*tool.path, err = system.commandPath(tool.name)
		if err != nil {
			return nil, fmt.Errorf("find Linux trust-store command %s: %w; install %s and the system file utilities", tool.name, err, backend.packageName)
		}
	}
	if system.euid() != 0 {
		store.sudoPath, err = system.commandPath("sudo")
		if err != nil {
			return nil, fmt.Errorf("find sudo: %w; install sudo or arrange root execution for cert-install", err)
		}
		args := []string{"-n", "-v"}
		var output io.Writer
		if system.interactive() {
			args = []string{"-v"}
			output = stderr
		}
		if err := system.run(store.sudoPath, args, output); err != nil {
			return nil, fmt.Errorf("authenticate sudo for the Linux system trust store: %w; retry polka cert-install in a terminal or configure noninteractive sudo access", err)
		}
	}
	return store, nil
}

// detectLinuxTrustStore reads only distribution identifiers, without executing
// os-release as a shell script. A recognized ID takes priority over ID_LIKE.
func detectLinuxTrustStore(release []byte) (linuxTrustStoreBackend, error) {
	values := make(map[string]string)
	scanner := bufio.NewScanner(bytes.NewReader(release))
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || (key != "ID" && key != "ID_LIKE") {
			continue
		}
		value = strings.TrimSpace(value)
		if strings.HasPrefix(value, "\"") {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				return linuxTrustStoreBackend{}, fmt.Errorf("parse os-release %s: %w", key, err)
			}
			value = unquoted
		} else if strings.HasPrefix(value, "'") {
			if len(value) < 2 || !strings.HasSuffix(value, "'") {
				return linuxTrustStoreBackend{}, fmt.Errorf("parse os-release %s: unclosed quote", key)
			}
			value = value[1 : len(value)-1]
		}
		values[key] = strings.ToLower(value)
	}
	if err := scanner.Err(); err != nil {
		return linuxTrustStoreBackend{}, fmt.Errorf("read os-release: %w", err)
	}
	identifiers := append([]string{values["ID"]}, strings.Fields(values["ID_LIKE"])...)
	for _, id := range identifiers {
		switch id {
		case "debian", "ubuntu":
			return linuxTrustStoreBackend{
				anchorDir: "/usr/local/share/ca-certificates", layoutDir: "/usr/local/share/ca-certificates",
				refreshCommand: "update-ca-certificates", removeArgs: []string{"--fresh"}, packageName: "ca-certificates",
			}, nil
		case "alpine":
			return linuxTrustStoreBackend{
				anchorDir: "/usr/local/share/ca-certificates", layoutDir: "/usr/local/share/ca-certificates",
				refreshCommand: "update-ca-certificates", packageName: "ca-certificates",
			}, nil
		case "fedora", "rhel", "centos", "rocky", "almalinux":
			return linuxTrustStoreBackend{
				anchorDir: "/etc/pki/ca-trust/source/anchors", layoutDir: "/etc/pki/ca-trust/source",
				refreshCommand: "update-ca-trust", refreshArgs: []string{"extract"}, removeArgs: []string{"extract"}, packageName: "ca-certificates",
			}, nil
		case "arch", "archlinux":
			return linuxTrustStoreBackend{
				anchorDir: "/etc/ca-certificates/trust-source/anchors", layoutDir: "/etc/ca-certificates/trust-source",
				refreshCommand: "update-ca-trust", refreshArgs: []string{"extract"}, removeArgs: []string{"extract"}, packageName: "ca-certificates-utils",
			}, nil
		case "suse", "opensuse", "opensuse-leap", "opensuse-tumbleweed", "sles", "sled":
			return linuxTrustStoreBackend{
				anchorDir: "/etc/pki/trust/anchors", layoutDir: "/etc/pki/trust",
				refreshCommand: "update-ca-certificates", packageName: "ca-certificates",
			}, nil
		}
	}
	return linuxTrustStoreBackend{}, fmt.Errorf("automatic Linux system trust-store installation is not supported for ID=%q ID_LIKE=%q; install the CA manually", values["ID"], values["ID_LIKE"])
}

// linuxTrustStoreCommandPath includes sbin even when absent from the user's
// PATH and never elevates an executable found in a project-controlled directory.
func linuxTrustStoreCommandPath(name string) (string, error) {
	for _, dir := range []string{"/usr/sbin", "/usr/bin", "/sbin", "/bin"} {
		path, err := exec.LookPath(filepath.Join(dir, name))
		if err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("%s is not executable in the system bin or sbin directories", name)
}

// runLinuxTrustStoreCommand captures diagnostics, streaming authentication
// output to the user's terminal only when interactive sudo validation is needed.
func runLinuxTrustStoreCommand(name string, args []string, interactiveOutput io.Writer) error {
	command := exec.Command(name, args...)
	var output bytes.Buffer
	var writer io.Writer = &output
	if interactiveOutput != nil {
		command.Stdin = os.Stdin
		writer = io.MultiWriter(&output, interactiveOutput)
	}
	command.Stdout, command.Stderr = writer, writer
	if err := command.Run(); err != nil {
		return formatTrustStoreCommandError(name, err, strings.TrimSpace(output.String()))
	}
	return nil
}

// run elevates only a resolved native command, never the Polka process or its
// access to private keys and the user's keyring.
func (store *linuxTrustStore) run(name string, args ...string) error {
	if store.sudoPath != "" {
		args = append([]string{"-n", "--", name}, args...)
		name = store.sudoPath
	}
	return store.system.run(name, args, nil)
}

// certificatePath names each CA independently, allowing users and custom cache
// directories to coexist without replacing another CA's trust-store entry.
func (store *linuxTrustStore) certificatePath(certificate *x509.Certificate) string {
	fingerprint := sha256.Sum256(certificate.Raw)
	return filepath.Join(store.backend.anchorDir, fmt.Sprintf("polka-local-ca-%x.crt", fingerprint))
}

// readCertificate accepts exactly one public CA PEM block, preventing accidental
// installation of a server certificate, bundle, or private-key material.
func (store *linuxTrustStore) readCertificate(path string) (*x509.Certificate, error) {
	data, err := store.system.readFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate %s: %w", path, err)
	}
	data = bytes.TrimSpace(data)
	block, rest := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" || !bytes.HasPrefix(data, []byte("-----BEGIN CERTIFICATE-----")) || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("CA certificate %s must contain exactly one CERTIFICATE PEM block", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse CA certificate %s: %w", path, err)
	}
	if !certificate.IsCA || !certificate.BasicConstraintsValid {
		return nil, fmt.Errorf("certificate %s is not a CA", path)
	}
	return certificate, nil
}

// checkCertificate refuses unexpected contents and nonregular managed entries
// before install or removal. An absent managed entry is an ordinary no-op.
func (store *linuxTrustStore) checkCertificate(path string, certificate *x509.Certificate) (bool, error) {
	info, err := store.system.lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect managed CA certificate %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("refuse managed CA certificate %s: not a regular file (symlinks are not allowed)", path)
	}
	existing, err := store.readCertificate(path)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(existing.Raw, certificate.Raw) {
		return false, fmt.Errorf("refuse managed CA certificate %s: certificate does not match its fingerprint", path)
	}
	return true, nil
}

// install copies only the public CA into the native administrator directory and
// reports success only after the system's derived trust stores are refreshed.
func (store *linuxTrustStore) install(certificatePath string) (string, error) {
	absolutePath, err := filepath.Abs(certificatePath)
	if err != nil {
		return "", fmt.Errorf("resolve CA certificate path: %w", err)
	}
	certificate, err := store.readCertificate(absolutePath)
	if err != nil {
		return "", err
	}
	target := store.certificatePath(certificate)
	if _, err := store.checkCertificate(target, certificate); err != nil {
		return "", err
	}
	if err := store.run(store.installPath, "-D", "-m", "0644", "--", absolutePath, target); err != nil {
		return "", fmt.Errorf("copy CA certificate to %s: %w", target, err)
	}
	if err := store.run(store.refreshPath, store.backend.refreshArgs...); err != nil {
		return "", fmt.Errorf("refresh Linux system trust store after installing %s: %w", target, err)
	}
	return "Linux system (" + store.backend.anchorDir + ")", nil
}

// remove deletes only the exact old CA's managed entry. Manually installed CAs
// and other Polka CAs are never searched for or removed by subject name.
func (store *linuxTrustStore) remove(certificate *x509.Certificate) (string, error) {
	if certificate == nil {
		return "", fmt.Errorf("remove CA certificate: missing certificate")
	}
	target := store.certificatePath(certificate)
	exists, err := store.checkCertificate(target, certificate)
	if err != nil {
		return "", err
	}
	if exists {
		if err := store.run(store.removePath, "-f", "--", target); err != nil {
			return "", fmt.Errorf("remove managed CA certificate %s: %w", target, err)
		}
		if err := store.run(store.refreshPath, store.backend.removeArgs...); err != nil {
			return "", fmt.Errorf("refresh Linux system trust store after removing %s: %w", target, err)
		}
	}
	return "Linux system (" + store.backend.anchorDir + ")", nil
}
