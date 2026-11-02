package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"polka/backend"
	"polka/tools"
)

// buildCommandInput holds the parsed flags of `polka build`.
type buildCommandInput struct {
	ConfigureArgs []string
	Jobs          int
	Force         bool
}

// buildCommandGOOS is the platform `polka build` checks, replaced in tests so
// the rejection path runs on every CI platform.
var buildCommandGOOS = runtime.GOOS

// buildToolFunc runs a build, replaced in tests so command behavior can be
// checked without a compiler.
var buildToolFunc = backend.Store.BuildTool

// newBuildCommand creates `polka build`. It is registered on every platform, so
// Windows users get an explanation instead of an unknown-command error.
func newBuildCommand(ctx *commandContext) *cobra.Command {
	var input buildCommandInput

	cmd := &cobra.Command{
		Use:  "build <tool:version>",
		Args: exactArgsError("build requires one tool:version argument, for example php:8.4", 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if buildCommandGOOS != "linux" {
				return &statusError{code: 1, err: fmt.Errorf("polka build is only supported on Linux")}
			}
			tool, version, err := parseBuildToolVersion(args[0])
			if err != nil {
				return &statusError{code: 1, err: err}
			}
			if cmd.Flags().Changed("jobs") && input.Jobs < 1 {
				return &statusError{code: 1, err: fmt.Errorf("--jobs must be at least 1")}
			}
			// Building needs only the cache and registry, so a directory with no
			// polka.yaml, or with one that does not parse, must not block it.
			store, err := ctx.initStore()
			if err != nil {
				return &statusError{code: 1, err: err}
			}

			signalContext, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stopSignals()

			return runBuildCommand(signalContext, cmd.OutOrStdout(), cmd.ErrOrStderr(), store, tool, version, input)
		},
	}
	cmd.Flags().StringArrayVar(&input.ConfigureArgs, "configure-arg", nil, "extra ./configure option; repeatable")
	cmd.Flags().IntVar(&input.Jobs, "jobs", 0, "parallel compile jobs (default: every CPU)")
	cmd.Flags().BoolVar(&input.Force, "force", false, "rebuild a label that is already cached")
	configureCommand(cmd, buildUsage)

	return cmd
}

// parseBuildToolVersion splits a tool:version argument, normalizing the tool ID.
func parseBuildToolVersion(value string) (string, string, error) {
	tool, version, ok := strings.Cut(strings.TrimSpace(value), ":")
	if !ok || strings.TrimSpace(tool) == "" || strings.TrimSpace(version) == "" || strings.Contains(version, ":") {
		return "", "", fmt.Errorf("build argument must be TOOL:VERSION, for example php:8.4")
	}

	return strings.ToLower(strings.TrimSpace(tool)), strings.TrimSpace(version), nil
}

// runBuildCommand compiles the tool and reports what happened and what to do
// next. Compiler output stays in the log; only short status lines print here.
func runBuildCommand(ctx context.Context, stdout, stderr io.Writer, store backend.Store, tool, version string, input buildCommandInput) error {
	result, err := buildToolFunc(store, ctx, tools.BuildRequest{
		Tool:          tool,
		Version:       version,
		ConfigureArgs: input.ConfigureArgs,
		Jobs:          input.Jobs,
		Force:         input.Force,
		Status: func(message string) {
			_, _ = fmt.Fprintln(stdout, message)
		},
		Warn: func(message string) {
			_, _ = fmt.Fprintf(stderr, "warning: %s\n", message)
		},
	})
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return &statusError{code: 130, err: fmt.Errorf("build of %s %s was cancelled", tool, version)}
		}
		return &statusError{code: 1, err: err}
	}

	if result.AlreadyCached {
		origin := "downloaded"
		if result.LocallyBuilt {
			origin = "locally built"
		}
		_, _ = fmt.Fprintf(stdout, "%s %s is already cached (%s, %s); pass --force to rebuild it\n", tool, version, result.ResolvedVersion, origin)
		return nil
	}

	_, _ = fmt.Fprintf(stdout, "Built %s %s (%s)\n", tool, version, result.ResolvedVersion)
	_, _ = fmt.Fprintf(stdout, "Payload: %s\n", result.PayloadPath)
	if result.Replaced {
		_, _ = fmt.Fprintf(stdout, "Existing project installs keep the previous build; run \"polka install --force\" in them to use this one.\n")
	} else {
		_, _ = fmt.Fprintf(stdout, "Set tools.%s to %s in a project and run \"polka install\" to use it.\n", tool, version)
	}

	return nil
}
