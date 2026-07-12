package cli

import (
	"github.com/spf13/cobra"
)

// shortcutSpec describes a command that expands to another command path with
// pre-filled leading arguments before any user-supplied arguments. For example,
// the "php" shortcut prefixes {"exec", "php"} so "polka php -v" runs as
// "polka exec php -v".
type shortcutSpec struct {
	use    string   // cobra Use, e.g. "php [args...]"
	short  string   // one-line help summary
	prefix []string // args prepended before the user's args, e.g. {"exec", "php"}
	hidden bool      // keep out of the help listing
	usage  string    // detailed help text (optional; falls back to the target's help)
}

// newShortcutCommand builds a cobra command that expands to the command path in
// spec.prefix. Flag parsing is disabled so every argument after the shortcut
// name passes through untouched, and the command re-dispatches through the root
// so persistent flags (such as --root) and the target command's own flag and
// argument validation continue to work unchanged.
func newShortcutCommand(spec shortcutSpec) *cobra.Command {
	cmd := &cobra.Command{
		Use:                spec.use,
		Short:              spec.short,
		Hidden:             spec.hidden,
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			root := cmd.Root()
			expanded := append(append([]string{}, spec.prefix...), args...)
			root.SetArgs(expanded)
			return root.Execute()
		},
	}
	if spec.usage != "" {
		configureHelp(cmd, spec.usage)
	}

	return cmd
}

// shortcutSpecs returns the shortcut commands registered on the root. The env
// shortcuts point at the real command implementations grouped under "env"; the
// tool shortcuts prepend "exec" so managed binaries can be invoked directly.
func shortcutSpecs() []shortcutSpec {
	return []shortcutSpec{
		{use: "new <name>", short: "shortcut for env new", prefix: []string{"env", "new"}, usage: newUsage},
		{use: "config [<key> <value>]", short: "shortcut for env config", prefix: []string{"env", "config"}, usage: configUsage},
		{use: "install [tool:version]", short: "shortcut for env install", prefix: []string{"env", "install"}, usage: installUsage},
		{use: "list", short: "shortcut for env list", prefix: []string{"env", "list"}, usage: listUsage},
		{use: "use <name>", short: "shortcut for env use", prefix: []string{"env", "use"}, usage: useUsage},
		{use: "remove <name>", short: "shortcut for env remove", prefix: []string{"env", "remove"}, usage: removeUsage},
		{use: "php [args...]", short: "run php with the local shell environment", prefix: []string{"exec", "php"}},
		{use: "composer [args...]", short: "run composer with the local shell environment", prefix: []string{"exec", "composer"}},
		{use: "node [args...]", prefix: []string{"exec", "node"}, hidden: true},
		{use: "npm [args...]", prefix: []string{"exec", "npm"}, hidden: true},
		{use: "drush [args...]", prefix: []string{"exec", "drush"}, hidden: true},
	}
}
