module polka.cli.command_shortcut;

// Declarative top-level shortcut definitions mapping familiar commands
// (`new`, `config`, `install`, `list`, `use`, `remove`, `php`, `composer`,
// and hidden `node`, `npm`, `drush`) onto their underlying `env` or `exec` paths.

import polka.cli.usage : configUsage, installUsage, listUsage, newUsage, removeUsage, useUsage;

/// Specification of a root shortcut command that prepends `prefix` before user args
/// and re-dispatches through the root router.
struct ShortcutSpec
{
    string name;
    string useLine;
    string shortDesc;
    string[] prefix;
    bool hidden = false;
    string usage;
}

/// Returns the registered root shortcut specifications in canonical order.
ShortcutSpec[] shortcutSpecs() @safe pure
{
    return [
        ShortcutSpec("new", "new <name>", "shortcut for env new", ["env", "new"], false, newUsage),
        ShortcutSpec("config", "config [<key> <value>]", "shortcut for env config", ["env", "config"], false, configUsage),
        ShortcutSpec("install", "install [tool:version]", "shortcut for env install", ["env", "install"], false, installUsage),
        ShortcutSpec("list", "list", "shortcut for env list", ["env", "list"], false, listUsage),
        ShortcutSpec("use", "use <name>", "shortcut for env use", ["env", "use"], false, useUsage),
        ShortcutSpec("remove", "remove <name>", "shortcut for env remove", ["env", "remove"], false, removeUsage),
        ShortcutSpec("php", "php [args...]", "run php with the local shell environment", ["exec", "php"]),
        ShortcutSpec("composer", "composer [args...]", "run composer with the local shell environment", ["exec", "composer"]),
        ShortcutSpec("node", "node [args...]", "", ["exec", "node"], true),
        ShortcutSpec("npm", "npm [args...]", "", ["exec", "npm"], true),
        ShortcutSpec("drush", "drush [args...]", "", ["exec", "drush"], true),
    ];
}

unittest
{
    auto specs = shortcutSpecs();
    assert(specs.length == 11);
    assert(specs[0].name == "new" && specs[0].prefix == ["env", "new"] && !specs[0].hidden);
    assert(specs[6].name == "php" && specs[6].prefix == ["exec", "php"] && !specs[6].hidden);
    assert(specs[8].name == "node" && specs[8].prefix == ["exec", "node"] && specs[8].hidden);
}
