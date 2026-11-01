package plugins

import (
	"fmt"
	"slices"
	"strings"
)

// phpToken retains byte spans so edits can leave all unrelated source intact.
// The scanner deliberately rejects constructs it cannot safely understand.
type phpToken struct {
	text, kind string
	start, end int
}
type phpDocument struct{ tokens, comments []phpToken }

// scanPHP recognizes strings and comments before punctuation, avoiding matches
// against example configuration inside comments or quoted application text.
func scanPHP(src string) (phpDocument, error) {
	var doc phpDocument
	inPHP := false
	for i := 0; i < len(src); {
		if !inPHP {
			n := strings.Index(src[i:], "<?")
			if n < 0 {
				break
			}
			i += n
			if !strings.HasPrefix(src[i:], "<?php") {
				return doc, fmt.Errorf("unsupported PHP opening tag")
			}
			doc.tokens = append(doc.tokens, phpToken{"<?php", "open", i, i + 5})
			i += 5
			inPHP = true
			continue
		}
		start := i
		c := src[i]
		if strings.ContainsRune(" \t\r\n", rune(c)) {
			i++
			continue
		}
		if strings.HasPrefix(src[i:], "?>") {
			doc.tokens = append(doc.tokens, phpToken{"?>", "close", i, i + 2})
			i += 2
			inPHP = false
			continue
		}
		if c == '#' || strings.HasPrefix(src[i:], "//") {
			for i < len(src) && src[i] != '\n' && src[i] != '\r' && !strings.HasPrefix(src[i:], "?>") {
				i++
			}
			doc.comments = append(doc.comments, phpToken{src[start:i], "comment", start, i})
			continue
		}
		if strings.HasPrefix(src[i:], "/*") {
			n := strings.Index(src[i+2:], "*/")
			if n < 0 {
				return doc, fmt.Errorf("unterminated PHP comment")
			}
			i += n + 4
			doc.comments = append(doc.comments, phpToken{src[start:i], "comment", start, i})
			continue
		}
		if strings.HasPrefix(src[i:], "<<<") || c == '`' {
			return doc, fmt.Errorf("unsupported PHP string syntax")
		}
		if c == '\'' || c == '"' {
			i++
			closed := false
			for i < len(src) {
				if src[i] == '\\' {
					i += 2
					continue
				}
				if src[i] == c {
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return doc, fmt.Errorf("unterminated PHP string")
			}
			doc.tokens = append(doc.tokens, phpToken{src[start:i], "string", start, i})
			continue
		}
		if isPHPIdentifier(c) {
			i++
			for i < len(src) && (isPHPIdentifier(src[i]) || (src[i] >= '0' && src[i] <= '9')) {
				i++
			}
			doc.tokens = append(doc.tokens, phpToken{src[start:i], "identifier", start, i})
			continue
		}
		i++
		for _, op := range []string{"=>", "->", "::", "..."} {
			if strings.HasPrefix(src[start:], op) {
				i = start + len(op)
				break
			}
		}
		doc.tokens = append(doc.tokens, phpToken{src[start:i], "symbol", start, i})
	}
	if len(doc.tokens) == 0 {
		return doc, fmt.Errorf("configuration has no PHP code")
	}
	for i := 0; i < len(doc.tokens); i++ {
		if slices.Contains([]string{"[", "(", "{"}, doc.tokens[i].text) {
			close, err := phpMatching(doc.tokens, i)
			if err != nil {
				return doc, err
			}
			i = close
		} else if slices.Contains([]string{"]", ")", "}"}, doc.tokens[i].text) {
			return doc, fmt.Errorf("unbalanced PHP brackets")
		}
	}
	return doc, nil
}

func isPHPIdentifier(c byte) bool {
	return c == '_' || c >= 128 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// phpLiteralKey decodes a non-interpolated quoted key using PHP's quote rules.
func phpLiteralKey(t phpToken) (string, error) {
	if t.kind != "string" {
		return "", fmt.Errorf("PHP configuration keys must be literal strings")
	}
	quote := t.text[0]
	var out strings.Builder
	for i := 1; i < len(t.text)-1; i++ {
		c := t.text[i]
		if quote == '"' && c == '$' {
			return "", fmt.Errorf("interpolated PHP keys are unsupported")
		}
		if c == '\\' && i+1 < len(t.text)-1 {
			next := t.text[i+1]
			if next == quote || next == '\\' || (quote == '"' && next == '$') {
				out.WriteByte(next)
				i++
				continue
			}
			if quote == '"' {
				if escaped, ok := map[byte]byte{'n': '\n', 'r': '\r', 't': '\t'}[next]; ok {
					out.WriteByte(escaped)
					i++
					continue
				}
				return "", fmt.Errorf("unsupported PHP key escape")
			}
		}
		out.WriteByte(c)
	}
	return out.String(), nil
}

// phpMatching balances all bracket kinds, not just the current array's kind.
func phpMatching(tokens []phpToken, open int) (int, error) {
	stack := []string{}
	for i := open; i < len(tokens); i++ {
		switch tokens[i].text {
		case "[", "(", "{":
			stack = append(stack, tokens[i].text)
		case "]", ")", "}":
			if len(stack) == 0 {
				return 0, fmt.Errorf("unbalanced PHP brackets")
			}
			want := map[string]string{"]": "[", ")": "(", "}": "{"}[tokens[i].text]
			if stack[len(stack)-1] != want {
				return 0, fmt.Errorf("unbalanced PHP brackets")
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return i, nil
			}
		}
	}
	return 0, fmt.Errorf("unterminated PHP expression")
}

// phpSegments splits comma-delimited expressions without entering nested calls
// or arrays. Empty trailing segments represent a normal trailing comma.
func phpSegments(tokens []phpToken, start, end int) ([][2]int, error) {
	var segments [][2]int
	begin := start
	for i := start; i < end; i++ {
		switch tokens[i].text {
		case "[", "(", "{":
			close, err := phpMatching(tokens, i)
			if err != nil || close >= end {
				return nil, fmt.Errorf("unbalanced PHP expression")
			}
			i = close
		case "]", ")", "}":
			return nil, fmt.Errorf("unbalanced PHP expression")
		case ",":
			if i == begin {
				return nil, fmt.Errorf("empty PHP expression")
			}
			segments = append(segments, [2]int{begin, i})
			begin = i + 1
		}
	}
	if begin < end {
		segments = append(segments, [2]int{begin, end})
	}
	return segments, nil
}

type phpArrayEntry struct {
	key         string
	first, last int
}
type phpArray struct {
	open, close int
	entries     []phpArrayEntry
}

// parsePHPArray parses only literal containers. Unrelated values remain opaque
// expressions and are preserved; dynamic keys and spread entries are refused.
func parsePHPArray(tokens []phpToken, start, end int) (phpArray, error) {
	open := start
	if open < end && strings.EqualFold(tokens[open].text, "array") {
		open++
	}
	if open >= end || (tokens[open].text != "[" && tokens[open].text != "(") || (open == start && tokens[open].text != "[") {
		return phpArray{}, fmt.Errorf("configuration path is not a literal PHP array")
	}
	close, err := phpMatching(tokens, open)
	if err != nil {
		return phpArray{}, err
	}
	if close+1 != end {
		return phpArray{}, fmt.Errorf("computed PHP arrays cannot be patched safely")
	}
	segments, err := phpSegments(tokens, open+1, close)
	if err != nil {
		return phpArray{}, err
	}
	array := phpArray{open: open, close: close}
	seen := map[string]bool{}
	for _, segment := range segments {
		first, last := segment[0], segment[1]
		if last-first < 3 || tokens[first+1].text != "=>" {
			return phpArray{}, fmt.Errorf("configuration array requires literal string keys")
		}
		key, err := phpLiteralKey(tokens[first])
		if err != nil {
			return phpArray{}, err
		}
		if seen[key] {
			return phpArray{}, fmt.Errorf("duplicate PHP array key %q", key)
		}
		seen[key] = true
		array.entries = append(array.entries, phpArrayEntry{key, first + 2, last})
	}
	return array, nil
}

// phpReturnedArray finds a single top-level literal return, skipping returns
// within functions and conditionals. An executable expression is never evaluated.
func phpReturnedArray(tokens []phpToken) (phpArray, error) {
	var found *phpArray
	for i := 0; i < len(tokens); i++ {
		if slices.Contains([]string{"[", "(", "{"}, tokens[i].text) {
			close, err := phpMatching(tokens, i)
			if err != nil {
				return phpArray{}, err
			}
			i = close
			continue
		}
		if !strings.EqualFold(tokens[i].text, "return") {
			continue
		}
		if i > 0 && slices.Contains([]string{"$", "->", "::"}, tokens[i-1].text) {
			continue
		}
		if found != nil {
			return phpArray{}, fmt.Errorf("multiple top-level PHP returns are unsupported")
		}
		start := i + 1
		open := start
		if open < len(tokens) && strings.EqualFold(tokens[open].text, "array") {
			open++
		}
		if open >= len(tokens) || (tokens[open].text != "[" && tokens[open].text != "(") {
			return phpArray{}, fmt.Errorf("configuration must return a literal PHP array")
		}
		close, err := phpMatching(tokens, open)
		if err != nil {
			return phpArray{}, err
		}
		if close+1 >= len(tokens) || tokens[close+1].text != ";" {
			return phpArray{}, fmt.Errorf("computed PHP return expressions are unsupported")
		}
		array, err := parsePHPArray(tokens, start, close+1)
		if err != nil {
			return phpArray{}, err
		}
		found = &array
		i = close + 1
	}
	if found == nil {
		return phpArray{}, fmt.Errorf("configuration must return a literal PHP array")
	}
	return *found, nil
}

func phpEOL(src string) string {
	if strings.Contains(src, "\r\n") {
		return "\r\n"
	}
	return "\n"
}
func phpLineStart(src string, pos int) int { return strings.LastIndex(src[:pos], "\n") + 1 }
func phpIndent(src string, pos int) string {
	line := src[phpLineStart(src, pos):pos]
	return line[:len(line)-len(strings.TrimLeft(line, " \t"))]
}

// phpNestedValue renders newly inserted literal branches; it never rewrites an
// existing container, which may carry application settings and comments.
func phpNestedValue(keys []string, value, indent, eol string) string {
	if len(keys) == 0 {
		return value
	}
	return "[" + eol + indent + "    " + phpSingleQuotedString(keys[0]) + " => " + phpNestedValue(keys[1:], value, indent+"    ", eol) + "," + eol + indent + "]"
}

// patchPHPArrayValue changes one leaf in memory. Callers write only after every
// declared update has been resolved and patched successfully.
func patchPHPArrayValue(src string, path []string, value string) (string, error) {
	if len(path) == 0 {
		return "", fmt.Errorf("PHP update path cannot be empty")
	}
	doc, err := scanPHP(src)
	if err != nil {
		return "", err
	}
	tokens := doc.tokens
	array, err := phpReturnedArray(tokens)
	if err != nil {
		return "", err
	}
	for depth, key := range path {
		var entry *phpArrayEntry
		for i := range array.entries {
			if array.entries[i].key == key {
				entry = &array.entries[i]
				break
			}
		}
		if entry == nil {
			close := tokens[array.close].start
			if len(array.entries) > 0 && tokens[array.close-1].text != "," {
				last := array.entries[len(array.entries)-1]
				end := tokens[last.last-1].end
				src = src[:end] + "," + src[end:]
				close++
			}
			eol := phpEOL(src)
			baseIndent := phpIndent(src, tokens[array.open].start)
			indent := baseIndent + "    "
			if len(array.entries) > 0 {
				firstKey := tokens[array.entries[0].first-2].start
				if candidate := phpIndent(src, firstKey); len(candidate) > len(baseIndent) {
					indent = candidate
				}
			}
			assignment := indent + phpSingleQuotedString(key) + " => " + phpNestedValue(path[depth+1:], value, indent, eol) + "," + eol
			lineStart := phpLineStart(src, close)
			if strings.TrimSpace(src[lineStart:close]) == "" {
				return src[:lineStart] + assignment + src[lineStart:], nil
			}
			return src[:close] + eol + assignment + baseIndent + src[close:], nil
		}
		if depth == len(path)-1 {
			return src[:tokens[entry.first].start] + value + src[tokens[entry.last-1].end:], nil
		}
		array, err = parsePHPArray(tokens, entry.first, entry.last)
		if err != nil {
			return "", fmt.Errorf("array path %q: %w", key, err)
		}
	}
	return src, nil
}

type phpEdit struct {
	start, end  int
	replacement string
}

// applyPHPEdits applies nonoverlapping source spans from right to left.
func applyPHPEdits(src string, edits []phpEdit) (string, error) {
	slices.SortFunc(edits, func(a, b phpEdit) int {
		if a.start > b.start {
			return -1
		}
		if a.start < b.start {
			return 1
		}
		return b.end - a.end
	})
	previous := len(src)
	for _, edit := range edits {
		if edit.start < 0 || edit.end < edit.start || edit.end > previous {
			return "", fmt.Errorf("overlapping PHP edits")
		}
		src = src[:edit.start] + edit.replacement + src[edit.end:]
		previous = edit.start
	}
	return src, nil
}

// patchPHPConstants replaces only the second argument of literal define calls.
// Comments, spacing, quote styles on keys, and unrelated defines survive.
func patchPHPConstants(src string, values map[string]string, order []string, anchor string) (string, error) {
	doc, err := scanPHP(src)
	if err != nil {
		return "", err
	}
	tokens := doc.tokens
	var edits []phpEdit
	written := map[string]bool{}
	insertAt, fallback := -1, len(src)
	depth := 0
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.text == "{" {
			depth++
		}
		if t.text == "}" {
			depth--
		}
		if depth == 0 && (t.kind == "close" || slices.Contains([]string{"require", "require_once", "include", "include_once"}, strings.ToLower(t.text))) {
			if t.start < fallback {
				fallback = t.start
			}
		}
		if !strings.EqualFold(t.text, "define") || i+1 >= len(tokens) || tokens[i+1].text != "(" {
			continue
		}
		if i > 0 && slices.Contains([]string{"$", "->", "::", "function"}, tokens[i-1].text) {
			continue
		}
		if i > 1 && tokens[i-1].text == "\\" && tokens[i-2].kind == "identifier" {
			continue
		}
		close, err := phpMatching(tokens, i+1)
		if err != nil {
			return "", err
		}
		args, err := phpSegments(tokens, i+2, close)
		if err != nil || len(args) < 2 || len(args) > 3 || args[0][1]-args[0][0] != 1 {
			return "", fmt.Errorf("unsupported PHP define arguments")
		}
		key, err := phpLiteralKey(tokens[args[0][0]])
		if err != nil {
			return "", err
		}
		if key == anchor && insertAt < 0 && depth == 0 {
			insertAt = t.start
		}
		if value, managed := values[key]; managed {
			if written[key] {
				return "", fmt.Errorf("duplicate PHP constant %q", key)
			}
			written[key] = true
			edits = append(edits, phpEdit{tokens[args[1][0]].start, tokens[args[1][1]-1].end, value})
		}
		i = close
	}
	if insertAt < 0 {
		insertAt = fallback
	}
	eol := phpEOL(src)
	var missing []string
	for _, key := range order {
		if !written[key] {
			missing = append(missing, "define( "+phpSingleQuotedString(key)+", "+values[key]+" );")
		}
	}
	if len(missing) > 0 {
		lineStart := phpLineStart(src, insertAt)
		if strings.TrimSpace(src[lineStart:insertAt]) == "" {
			insertAt = lineStart
		}
		block := strings.Join(missing, eol) + eol
		if insertAt > 0 && src[insertAt-1] != '\n' {
			block = eol + block
		}
		edits = append(edits, phpEdit{insertAt, insertAt, block})
	}
	return applyPHPEdits(src, edits)
}

// patchPHPInclude keeps the managed block after application settings so that
// installer-written credentials cannot override it. Markers are recognized only
// in actual line comments; malformed blocks are refused instead of duplicated.
func patchPHPInclude(src, include, marker string) (string, error) {
	doc, err := scanPHP(src)
	if err != nil {
		return "", err
	}
	startMarker, endMarker := "// <polka:"+marker+">", "// </polka:"+marker+">"
	start, end := -1, -1
	for _, comment := range doc.comments {
		switch comment.text {
		case startMarker:
			if start >= 0 {
				return "", fmt.Errorf("duplicate PHP include marker")
			}
			start = comment.start
		case endMarker:
			if end >= 0 {
				return "", fmt.Errorf("duplicate PHP include marker")
			}
			end = comment.end
		}
	}
	if (start >= 0) != (end >= 0) || (end >= 0 && end < start) {
		return "", fmt.Errorf("incomplete PHP include markers")
	}
	eol := phpEOL(src)
	file := phpSingleQuotedString("/" + include)
	block := strings.Join([]string{startMarker, "if (file_exists(__DIR__ . " + file + ")) {", "  include __DIR__ . " + file + ";", "}", endMarker}, eol)
	position := len(src)
	for _, token := range doc.tokens {
		if token.kind == "open" {
			position = len(src)
		}
		if token.kind == "close" {
			position = token.start
		}
	}
	if start >= 0 {
		if strings.TrimSpace(src[end:position]) == "" {
			return src[:start] + block + src[end:], nil
		}
		// Remove the old block's final newline along with the block, leaving
		// surrounding application settings intact when it moves to the end.
		if strings.HasPrefix(src[end:], eol) {
			end += len(eol)
		}
		src = src[:start] + src[end:]
		position -= end - start
	}
	if position > 0 && src[position-1] != '\n' {
		block = eol + block
	}
	return src[:position] + block + eol + src[position:], nil
}

// renderPHPScalar is the sole path from managed scalar data into PHP source.
// null, bool, and numeric literals retain their types; strings are quoted.
func renderPHPScalar(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "null", nil
	case string:
		return phpSingleQuotedString(v), nil
	case bool:
		if v {
			return "true", nil
		}
		return "false", nil
	case int, int64, uint64, float64:
		text := fmt.Sprint(v)
		if strings.ContainsAny(strings.ToLower(text), "naif") {
			return "", fmt.Errorf("non-finite PHP numeric value")
		}
		return text, nil
	default:
		return "", fmt.Errorf("unsupported PHP scalar type %T", value)
	}
}
