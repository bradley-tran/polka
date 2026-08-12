package cli

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// composerCommandWorkingDir applies Composer's -d/--working-dir option so the
// manifest and referenced script paths are resolved from the same directory.
func composerCommandWorkingDir(workingDir string, args []string) string {
	resolved := workingDir
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "--" {
			break
		}
		var value string
		switch {
		case arg == "-d" || arg == "--working-dir":
			if index+1 < len(args) {
				value = args[index+1]
				index++
			}
		case strings.HasPrefix(arg, "--working-dir="):
			value = strings.TrimPrefix(arg, "--working-dir=")
		case strings.HasPrefix(arg, "-d") && len(arg) > len("-d"):
			value = strings.TrimPrefix(arg, "-d")
		}
		if strings.TrimSpace(value) == "" {
			continue
		}
		if filepath.IsAbs(value) {
			resolved = value
		} else {
			resolved = filepath.Join(workingDir, value)
		}
	}

	return filepath.Clean(resolved)
}

// composerManifestPath honors COMPOSER when it names an alternate manifest.
func composerManifestPath(goos, workingDir string, env []string) string {
	_, value, ok := lookupEnvValue(goos, env, "COMPOSER")
	value = strings.TrimSpace(value)
	if !ok || value == "" || value == "-" {
		return filepath.Join(workingDir, "composer.json")
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}

	return filepath.Join(workingDir, value)
}

// composerManifestScriptCommands extracts strings from scalar and array-valued
// script definitions while ignoring Composer's optional description metadata.
func composerManifestScriptCommands(data []byte) ([]string, error) {
	var manifest struct {
		Scripts map[string]json.RawMessage `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, err
	}

	names := make([]string, 0, len(manifest.Scripts))
	for name := range manifest.Scripts {
		names = append(names, name)
	}
	sort.Strings(names)

	var commands []string
	for _, name := range names {
		raw := manifest.Scripts[name]
		var command string
		if err := json.Unmarshal(raw, &command); err == nil {
			commands = append(commands, command)
			continue
		}

		var commandList []string
		if err := json.Unmarshal(raw, &commandList); err == nil {
			commands = append(commands, commandList...)
		}
	}

	return commands, nil
}

// composerDirectPHPScriptTarget returns the first command word when it resolves
// inside the Composer working directory to an extensionless PHP-shebang file.
func composerDirectPHPScriptTarget(workingDir, command string) (string, bool) {
	word := firstComposerScriptWord(command)
	if word == "" || strings.HasPrefix(word, "@") || filepath.Ext(word) != "" {
		return "", false
	}

	target := word
	if !filepath.IsAbs(target) {
		target = filepath.Join(workingDir, filepath.FromSlash(target))
	}
	target = filepath.Clean(target)
	relative, err := filepath.Rel(workingDir, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}

	line, err := readScriptFirstLine(target)
	if err != nil || !isPHPShebang(line) {
		return "", false
	}

	return target, true
}

// firstComposerScriptWord reads one shell-style word and supports the quoting
// used for script paths without attempting to reinterpret the full command.
func firstComposerScriptWord(command string) string {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return ""
	}

	var word strings.Builder
	var quote rune
	for _, current := range trimmed {
		switch {
		case quote != 0:
			if current == quote {
				quote = 0
			} else {
				word.WriteRune(current)
			}
		case current == '\'' || current == '"':
			quote = current
		case current == ' ' || current == '\t' || current == '\r' || current == '\n':
			return word.String()
		default:
			word.WriteRune(current)
		}
	}

	return word.String()
}
