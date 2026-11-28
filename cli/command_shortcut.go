package cli

import (
	"context"

	ucli "github.com/urfave/cli/v3"
)

// shortcutSpec describes a command that expands to another command path with
// pre-filled leading arguments before any user-supplied arguments. For example,
// the "php" shortcut prefixes {"exec", "php"} so "polka php -v" runs as
// "polka exec php -v".
type shortcutSpec struct {
	name   string   // command name, e.g. "php"
	short  string   // one-line help summary
	prefix []string // args prepended before the user's args, e.g. {"exec", "php"}
	hidden bool     // keep out of the help listing
	usage  string   // detailed help text (optional; falls back to the target's help)
}

// newShortcutCommand builds a urfave/cli command that expands to the command
// path in spec.prefix. Flag parsing is disabled so every argument after the
// shortcut name passes through untouched, and the command re-dispatches through
// a fresh root command so persistent flags (such as -C/--dir) and the target
// command's own flag and argument validation continue to work unchanged.
func newShortcutCommand(ctx *commandContext, spec shortcutSpec) *ucli.Command {
	cmd := &ucli.Command{
		Name:            spec.name,
		Usage:           spec.short,
		Hidden:          spec.hidden,
		SkipFlagParsing: true,
		Action: func(runCtx context.Context, cmd *ucli.Command) error {
			expanded := append(append([]string{"polka"}, spec.prefix...), cmd.Args().Slice()...)
			return newRootCommand(ctx).Run(runCtx, expanded)
		},
	}
	if spec.usage != "" {
		configureHelp(cmd, spec.usage)
	} else {
		// Tool shortcuts forward --help / -h to the underlying tool via exec.
		cmd.HideHelp = true
	}

	return cmd
}

// shortcutSpecs returns the shortcut commands registered on the root. The env
// shortcuts point at the real command implementations grouped under "env"; the
// tool shortcuts prepend "exec" so managed binaries can be invoked directly.
func shortcutSpecs() []shortcutSpec {
	return []shortcutSpec{
		{name: "new", short: "shortcut for env new", prefix: []string{"env", "new"}, usage: newUsage},
		{name: "config", short: "shortcut for env config", prefix: []string{"env", "config"}, usage: configUsage},
		{name: "install", short: "shortcut for env install", prefix: []string{"env", "install"}, usage: installUsage},
		{name: "list", short: "shortcut for env list", prefix: []string{"env", "list"}, usage: listUsage},
		{name: "use", short: "shortcut for env use", prefix: []string{"env", "use"}, usage: useUsage},
		{name: "remove", short: "shortcut for env remove", prefix: []string{"env", "remove"}, usage: removeUsage},
		{name: "php", short: "run php with the local shell environment", prefix: []string{"exec", "php"}},
		{name: "composer", short: "run composer with the local shell environment", prefix: []string{"exec", "composer"}},
		{name: "node", prefix: []string{"exec", "node"}, hidden: true},
		{name: "npm", prefix: []string{"exec", "npm"}, hidden: true},
		{name: "drush", prefix: []string{"exec", "drush"}, hidden: true},
	}
}
