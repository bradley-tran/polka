# Resolves the PHP devel pack matching a managed Windows runtime, and patches
# its build scripts so PHP_PREFIX resolves to the matching Polka runtime instead
# of the upstream hardcoded C:\php.

load("php_windows.star", "asset_plan", "has_asset", "select_build")

_PATCH_MARKER = "// Polka auto-patch: dynamically resolve PHP_PREFIX to the matching Polka PHP runtime"

_PHP_DIR_LINE = r"(?m)^(var\s+PHP_DIR\s*=\s*FSO\.GetParentFolderName\(WScript\.ScriptFullName\)\.replace\([^\)]+\);?\r?)$"
_PHP_PREFIX_BLOCK = r"(?s)if\s*\(\s*PHP_PREFIX\s*==\s*''\s*\)\s*\{[\s\S]*?DEFINE\(\s*'PHP_PREFIX'\s*,\s*PHP_PREFIX\s*\);"

# Runs after PHP_DIR is set in phpize.js. Lines join with the file's own ending.
_PHPIZE_SNIPPET = [
    "",
    _PATCH_MARKER,
    "(function() {",
    "\ttry {",
    "\t\tvar envShell = WScript.CreateObject(\"WScript.Shell\");",
    "\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");",
    "\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {",
    "\t\t\tPHP_PREFIX = envPrefix;",
    "\t\t\treturn;",
    "\t\t}",
    "\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));",
    "\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";",
    "\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);",
    "\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {",
    "\t\t\tPHP_PREFIX = polkaCandidate;",
    "\t\t}",
    "\t} catch (e) {}",
    "})();",
]

# Replaces the PHP_PREFIX block in config.w32.phpize.in.
_CONFIG_REPLACEMENT = [
    "if (PHP_PREFIX == '' || PHP_PREFIX == \"C:\\\\php\") {",
    "\t" + _PATCH_MARKER,
    "\ttry {",
    "\t\tvar envShell = WshShell;",
    "\t\tvar envPrefix = envShell.Environment(\"Process\").Item(\"PHP_PREFIX\");",
    "\t\tif (envPrefix && FSO.FileExists(FSO.BuildPath(envPrefix, \"php.exe\"))) {",
    "\t\t\tPHP_PREFIX = envPrefix;",
    "\t\t} else {",
    "\t\t\tvar polkaEnvsDir = FSO.GetParentFolderName(FSO.GetParentFolderName(PHP_DIR));",
    "\t\t\tvar polkaFlavor = (typeof(PHP_ZTS) == \"string\" && PHP_ZTS.toLowerCase() == \"yes\") ? \"php-zts\" : \"php\";",
    "\t\t\tvar polkaCandidate = FSO.BuildPath(FSO.BuildPath(polkaEnvsDir, polkaFlavor), PHP_VERSION + \".\" + PHP_MINOR_VERSION);",
    "\t\t\tif (FSO.FileExists(FSO.BuildPath(polkaCandidate, \"php.exe\"))) {",
    "\t\t\t\tPHP_PREFIX = polkaCandidate;",
    "\t\t\t}",
    "\t\t}",
    "\t} catch (e) {}",
    "\tif (PHP_PREFIX == '') {",
    "\t\tPHP_PREFIX = \"C:\\\\php\";",
    "\t}",
    "\tif (PHP_DEBUG == \"yes\")",
    "\t\tPHP_PREFIX += \"\\\\debug\";",
    "}",
    "DEFINE('PHP_PREFIX', PHP_PREFIX);",
]

def _line_ending(text):
    """Returns the line ending a patched file already uses."""
    if "\r\n" in text:
        return "\r\n"
    return "\n"

def _patch_phpize(text):
    """Inserts the runtime lookup right after the PHP_DIR line."""
    match = re.search(_PHP_DIR_LINE, text)
    if match == None:
        fail("patch phpize.js: could not find PHP_DIR anchor line in phpize.js")
    return text.replace(match[0], match[0] + _line_ending(text).join(_PHPIZE_SNIPPET), 1)

def _patch_config(text):
    """Replaces the upstream PHP_PREFIX default block."""
    match = re.search(_PHP_PREFIX_BLOCK, text)
    if match == None:
        fail("patch config.w32.phpize.in: could not find PHP_PREFIX block in config.w32.phpize.in")
    return text.replace(match[0], _line_ending(text).join(_CONFIG_REPLACEMENT), 1)

def post_install(ctx):
    writes = {}
    for file, patch in [("script/phpize.js", _patch_phpize), ("script/config.w32.phpize.in", _patch_config)]:
        text = ctx.read_text(file)
        if text == None:
            fail("patch php-devel: file %r not found in the install directory" % file)
        # The hook also runs on skipped installs, so patched files stay as is.
        if _PATCH_MARKER not in text:
            writes[file] = patch(text)
    return {"write": writes}

def _parse_cache_version(label):
    """Separates the backend's cache-only flavor suffix from the PHP version."""
    if label.endswith("-zts"):
        return label[:-len("-zts")], True
    if label.endswith("-nts"):
        return label[:-len("-nts")], False
    return label, False

def resolve(ctx):
    if ctx.os != "windows":
        fail("automatic php-devel download is only implemented on Windows")

    requested, thread_safe = _parse_cache_version(ctx.requested)
    index_url = ctx.vars["release-index"]
    label, variant = select_build(index_url, requested, thread_safe)
    if not has_asset(variant, "devel_pack"):
        fail("Windows PHP %s release does not publish a matching devel pack" % label)
    return asset_plan(index_url, label, variant, "devel_pack")
