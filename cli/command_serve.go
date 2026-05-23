package cli

import (
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
)

const (
	defaultServeHostname = "localhost"
	defaultServePort     = 8000
)

type serveCommandInput struct {
	Docroot string
	Server  string
}

func newServeCommand(ctx *commandContext) *cobra.Command {
	var input serveCommandInput

	cmd := &cobra.Command{
		Use:  "serve <docroot>",
		Args: exactArgsError("serve requires exactly one docroot", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := ctx.store()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			input.Docroot = strings.TrimSpace(args[0])
			input.Server = strings.TrimSpace(input.Server)
			if cmd.Flags().Changed("server") && input.Server == "" {
				return &statusError{code: 1, err: fmt.Errorf("--server requires a non-empty value")}
			}

			ctx.exitCode = runServe(cmd.OutOrStdout(), cmd.ErrOrStderr(), store, input)
			return nil
		},
	}
	cmd.Flags().StringVar(&input.Server, "server", "", "server address override in HOST:PORT form")
	configureCommand(cmd, serveUsage)

	return cmd
}

func runServe(stdout, stderr io.Writer, store backend.Store, input serveCommandInput) int {
	current, err := store.Current()
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if current == nil {
		fmt.Fprintln(stderr, "error: no active environment selected")
		return 1
	}

	phpTarget, err := store.ResolveTool("php")
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	serverAddress, err := resolveServeAddress(current.Server, input.Server)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	docroot, err := resolveServeDocroot(store.ProjectDir, input.Docroot)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	serveArgs := []string{"-S", serverAddress, "-t", docroot}
	exitCode, err := executeTarget(stdout, stderr, phpTarget, serveArgs)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	return exitCode
}

func resolveServeAddress(config *backend.ServerConfig, override string) (string, error) {
	if strings.TrimSpace(override) != "" {
		host, port, err := splitServerAddress(override)
		if err != nil {
			return "", err
		}

		return net.JoinHostPort(host, strconv.Itoa(port)), nil
	}

	host := defaultServeHostname
	port := defaultServePort
	if config != nil {
		if strings.TrimSpace(config.Hostname) != "" {
			host = strings.TrimSpace(config.Hostname)
		}
		if config.Port != 0 {
			port = config.Port
		}
	}
	if err := validateServeHostAndPort(host, port); err != nil {
		return "", err
	}

	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func splitServerAddress(value string) (string, int, error) {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(value))
	if err != nil {
		return "", 0, fmt.Errorf("invalid --server value %q: use HOST:PORT", value)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, fmt.Errorf("invalid --server value %q: use HOST:PORT", value)
	}
	if err := validateServeHostAndPort(host, port); err != nil {
		return "", 0, err
	}

	return host, port, nil
}

func validateServeHostAndPort(host string, port int) error {
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("server hostname cannot be empty")
	}
	if port < 1 || port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}

	return nil
}

func resolveServeDocroot(projectDir, docroot string) (string, error) {
	trimmed := strings.TrimSpace(docroot)
	if trimmed == "" {
		return "", fmt.Errorf("serve requires exactly one docroot")
	}

	resolved := filepath.Clean(trimmed)
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(projectDir, resolved)
	}

	fileInfo, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat docroot: %w", err)
	}
	if !fileInfo.IsDir() {
		return "", fmt.Errorf("docroot %q is not a directory", docroot)
	}

	return resolved, nil
}
