package plugins

import (
	"fmt"
	"io/fs"
	"maps"
	"net"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// frameworkValue is a scalar literal or exactly one structured expression.
// References and formatters operate on data; they have no I/O capabilities.
type frameworkValue struct {
	literal  any
	From     string                    `yaml:"from"`
	ByEngine map[string]frameworkValue `yaml:"by-engine"`
	Format   *frameworkFormat          `yaml:"format"`
	Default  *frameworkValue           `yaml:"default"`
}

// frameworkFormat exposes a small, typed set of reusable transformations.
// Framework-specific constants and per-engine policy remain in the manifest.
type frameworkFormat struct {
	Kind        string                    `yaml:"kind"`
	Value       *frameworkValue           `yaml:"value"`
	Host        *frameworkValue           `yaml:"host"`
	Port        *frameworkValue           `yaml:"port"`
	Scheme      *frameworkValue           `yaml:"scheme"`
	User        *frameworkValue           `yaml:"user"`
	Password    *frameworkValue           `yaml:"password"`
	Path        *frameworkValue           `yaml:"path"`
	Query       map[string]frameworkValue `yaml:"query"`
	OmitPort    string                    `yaml:"omit-port"`
	Prefix      string                    `yaml:"prefix"`
	StripPrefix string                    `yaml:"strip-prefix"`
	MinParts    int                       `yaml:"min-parts"`
}

// UnmarshalYAML decodes literals without losing their types, and uses strict
// field checking recursively for structured expressions.
func (v *frameworkValue) UnmarshalYAML(data []byte) error {
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	if _, mapping := raw.(map[string]any); !mapping {
		switch raw.(type) {
		case nil, string, bool, int, int64, uint64, float64:
			*v = frameworkValue{literal: raw}
			return nil
		default:
			return fmt.Errorf("value must be a scalar or a structured binding")
		}
	}
	type fields frameworkValue
	var decoded fields
	if err := yaml.UnmarshalWithOptions(data, &decoded, yaml.Strict()); err != nil {
		return err
	}
	*v = frameworkValue(decoded)
	forms := 0
	if v.From != "" {
		forms++
	}
	if v.ByEngine != nil {
		forms++
	}
	if v.Format != nil {
		forms++
	}
	if forms != 1 {
		return fmt.Errorf("binding requires exactly one of from, by-engine, or format")
	}
	return nil
}

// children enumerates expression operands for reference and cycle validation.
func (v frameworkValue) children() []frameworkValue {
	children := make([]frameworkValue, 0)
	if v.Default != nil {
		children = append(children, *v.Default)
	}
	for _, key := range slices.Sorted(maps.Keys(v.ByEngine)) {
		children = append(children, v.ByEngine[key])
	}
	if f := v.Format; f != nil {
		for _, child := range []*frameworkValue{f.Value, f.Host, f.Port, f.Scheme, f.User, f.Password, f.Path} {
			if child != nil {
				children = append(children, *child)
			}
		}
		for _, key := range slices.Sorted(maps.Keys(f.Query)) {
			children = append(children, f.Query[key])
		}
	}
	return children
}

// validate checks formatter operands and all expression branches recursively.
func (v frameworkValue) validate() error {
	for engine := range v.ByEngine {
		if !slices.Contains([]string{"mysql", "mariadb", "postgresql", "sqlite"}, engine) {
			return fmt.Errorf("unsupported by-engine key %q", engine)
		}
	}
	if f := v.Format; f != nil {
		switch f.Kind {
		case "string", "version":
			if f.Value == nil {
				return fmt.Errorf("%s formatter requires value", f.Kind)
			}
			if f.Host != nil || f.Port != nil || f.Scheme != nil || f.User != nil || f.Password != nil || f.Path != nil || len(f.Query) != 0 || f.OmitPort != "" {
				return fmt.Errorf("unexpected fields in %s formatter", f.Kind)
			}
			if f.Kind == "string" && (f.Prefix != "" || f.StripPrefix != "" || f.MinParts != 0) {
				return fmt.Errorf("version fields require version formatter")
			}
			if f.MinParts < 0 || f.MinParts > 8 {
				return fmt.Errorf("version min-parts must be between 0 and 8")
			}
		case "host-port", "url":
			if f.Host == nil {
				return fmt.Errorf("%s formatter requires host", f.Kind)
			}
			if f.Kind == "url" && (f.Scheme == nil || f.Path == nil) {
				return fmt.Errorf("url formatter requires scheme and path")
			}
			if f.Value != nil || f.Prefix != "" || f.StripPrefix != "" || f.MinParts != 0 {
				return fmt.Errorf("unexpected fields in %s formatter", f.Kind)
			}
			if f.Kind == "host-port" && (f.Scheme != nil || f.User != nil || f.Password != nil || f.Path != nil || len(f.Query) != 0) {
				return fmt.Errorf("URL fields require url formatter")
			}
			if f.Kind == "url" && f.OmitPort != "" {
				return fmt.Errorf("omit-port requires host-port formatter")
			}
			if f.OmitPort != "" {
				port, err := strconv.Atoi(f.OmitPort)
				if err != nil || port < 1 || port > 65535 {
					return fmt.Errorf("omit-port must be a valid port")
				}
			}
		default:
			return fmt.Errorf("unsupported formatter %q", f.Kind)
		}
	}
	for _, child := range v.children() {
		if err := child.validate(); err != nil {
			return err
		}
	}
	return nil
}

// validateValues checks references across named and runtime values as a graph,
// including unused engine branches, before any plugin can execute.
func (m frameworkManifest) validateValues() error {
	checker, err := newFrameworkValueChecker(m)
	if err != nil {
		return err
	}
	for _, key := range slices.Sorted(maps.Keys(checker.named)) {
		if err := checker.visit(key); err != nil {
			return err
		}
	}
	return checker.checkActions(m.PostComposer.Actions)
}

// validateActions applies the checks declared actions get at load time to
// actions a script planned, so nothing has run when an invalid plan is refused.
func (m frameworkManifest) validateActions(actions []frameworkActionManifest, files fs.FS) error {
	checker, err := newFrameworkValueChecker(m)
	if err != nil {
		return err
	}
	if err := checker.checkActions(actions); err != nil {
		return err
	}
	for i, action := range actions {
		if err := action.validate(files); err != nil {
			return fmt.Errorf("actions[%d]: %w", i, err)
		}
	}
	return nil
}

// frameworkValueChecker validates bindings against one manifest's named values.
type frameworkValueChecker struct {
	manifest frameworkManifest
	// named holds every referenceable value by its full reference.
	named map[string]frameworkValue
	// state tracks named values during traversal: 1 is in progress, which
	// identifies cycles, and 2 is done.
	state map[string]int
}

// newFrameworkValueChecker collects a manifest's referenceable values.
func newFrameworkValueChecker(m frameworkManifest) (*frameworkValueChecker, error) {
	named := map[string]frameworkValue{}
	for key, value := range m.Values {
		named["values."+key] = value
	}
	for key, value := range m.RuntimeEnv.Values {
		if !validFrameworkVariable(key) {
			return nil, fmt.Errorf("invalid runtime variable name %q", key)
		}
		named["runtime-env."+key] = value
	}
	return &frameworkValueChecker{manifest: m, named: named, state: map[string]int{}}, nil
}

// check validates one binding, its references, and all of its branches.
func (c *frameworkValueChecker) check(v frameworkValue) error {
	if err := v.validate(); err != nil {
		return err
	}
	if v.ByEngine != nil {
		for _, engine := range c.manifest.DatabaseEngines {
			if _, ok := v.ByEngine[engine]; !ok {
				return fmt.Errorf("by-engine is missing declared database engine %q", engine)
			}
		}
	}
	if v.From != "" {
		if strings.HasPrefix(v.From, "database.") {
			if !slices.Contains([]string{"engine", "version", "host", "port", "name", "user", "password"}, strings.TrimPrefix(v.From, "database.")) {
				return fmt.Errorf("unknown reference %q", v.From)
			}
		} else if _, exists := c.named[v.From]; !exists {
			return fmt.Errorf("unknown reference %q", v.From)
		} else if err := c.visit(v.From); err != nil {
			return err
		}
	}
	for _, child := range v.children() {
		if err := c.check(child); err != nil {
			return err
		}
	}
	return nil
}

// visit validates a named value once and detects reference cycles.
func (c *frameworkValueChecker) visit(key string) error {
	if c.state[key] == 1 {
		return fmt.Errorf("reference cycle at %q", key)
	}
	if c.state[key] == 2 {
		return nil
	}
	c.state[key] = 1
	if err := c.check(c.named[key]); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	c.state[key] = 2
	return nil
}

// checkActions validates the bindings and dotenv names of file actions.
func (c *frameworkValueChecker) checkActions(actions []frameworkActionManifest) error {
	for i, action := range actions {
		for key, value := range action.Values {
			if action.Type == "dotenv" && !validFrameworkVariable(key) {
				return fmt.Errorf("invalid dotenv variable name %q", key)
			}
			if err := c.check(value); err != nil {
				return fmt.Errorf("action %d value %q: %w", i, key, err)
			}
		}
		for _, update := range action.Updates {
			for _, value := range update.Values {
				if err := c.check(value); err != nil {
					return fmt.Errorf("action %d: %w", i, err)
				}
			}
		}
	}
	return nil
}

// validFrameworkVariable also permits dotted CodeIgniter dotenv keys.
func validFrameworkVariable(key string) bool {
	if key == "" {
		return false
	}
	for i, c := range key {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || (i > 0 && ((c >= '0' && c <= '9') || c == '.')) {
			continue
		}
		return false
	}
	return true
}

// frameworkValueState caches evaluated references for one hook invocation;
// no mutable values are retained on a registered plugin.
type frameworkValueState struct {
	manifest frameworkManifest
	database *DatabaseCredentials
	memo     map[string]any
}

// newFrameworkValueState normalizes a private credential copy for one invocation.
func newFrameworkValueState(m frameworkManifest, ctx RuntimeEnvContext) *frameworkValueState {
	var db *DatabaseCredentials
	if ctx.Database != nil {
		copy := *ctx.Database
		if copy.Engine == "" && ctx.Environment.Database != nil {
			copy.Engine = ctx.Environment.Database.Engine
		}
		db = normalizeDatabaseCredentials(&copy)
	}
	if db != nil {
		if db.Engine == "" {
			db.Engine = "mysql"
		}
		if ctx.Environment.Database != nil && ctx.Environment.Database.Version != "" {
			db.Version = ctx.Environment.Database.Version
		} else if version := ctx.Environment.ToolVersion(db.Engine); version != "" {
			db.Version = version
		}
	}
	return &frameworkValueState{manifest: m, database: db, memo: map[string]any{}}
}

// reference resolves a validated selector, memoizing named values for this call.
func (s *frameworkValueState) reference(ref string) (any, error) {
	if value, ok := s.memo[ref]; ok {
		return value, nil
	}
	if strings.HasPrefix(ref, "database.") {
		if s.database == nil {
			return nil, fmt.Errorf("reference %q requires managed database credentials", ref)
		}
		db := s.database
		values := map[string]string{"engine": db.Engine, "version": db.Version, "host": db.Host, "port": strconv.Itoa(db.Port), "name": db.DatabaseName, "user": db.User, "password": db.Password}
		value, ok := values[strings.TrimPrefix(ref, "database.")]
		if !ok {
			return nil, fmt.Errorf("unknown reference %q", ref)
		}
		return value, nil
	}
	var value frameworkValue
	var ok bool
	if strings.HasPrefix(ref, "values.") {
		value, ok = s.manifest.Values[strings.TrimPrefix(ref, "values.")]
	} else if strings.HasPrefix(ref, "runtime-env.") {
		value, ok = s.manifest.RuntimeEnv.Values[strings.TrimPrefix(ref, "runtime-env.")]
	}
	if !ok {
		return nil, fmt.Errorf("unknown reference %q", ref)
	}
	result, err := s.eval(value)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", ref, err)
	}
	s.memo[ref] = result
	return result, nil
}

// eval interprets one scalar expression and applies an optional empty-value default.
func (s *frameworkValueState) eval(v frameworkValue) (any, error) {
	result := v.literal
	var err error
	switch {
	case v.From != "":
		result, err = s.reference(v.From)
	case v.ByEngine != nil:
		if s.database == nil {
			return nil, fmt.Errorf("by-engine requires managed database credentials")
		}
		branch, ok := v.ByEngine[s.database.Engine]
		if !ok {
			return nil, fmt.Errorf("by-engine has no value for engine %q", s.database.Engine)
		}
		result, err = s.eval(branch)
	case v.Format != nil:
		result, err = s.format(*v.Format)
	}
	if err != nil {
		return nil, err
	}
	if v.Default != nil && (result == nil || result == "") {
		return s.eval(*v.Default)
	}
	return result, nil
}

// text resolves an optional formatter operand without exposing its value in errors.
func (s *frameworkValueState) text(v *frameworkValue) (string, error) {
	if v == nil {
		return "", nil
	}
	value, err := s.eval(*v)
	if err != nil {
		return "", err
	}
	return frameworkScalarText(value), nil
}

func frameworkScalarText(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

// format uses net/url for component escaping; credentials are never composed
// by unescaped string interpolation into a connection URL.
func (s *frameworkValueState) format(f frameworkFormat) (string, error) {
	if f.Kind == "string" || f.Kind == "version" {
		value, err := s.text(f.Value)
		if err != nil || f.Kind == "string" {
			return value, err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", nil
		}
		if strings.HasPrefix(strings.ToLower(value), strings.ToLower(f.StripPrefix)) {
			value = strings.TrimSpace(value[len(f.StripPrefix):])
		}
		parts := strings.Split(value, ".")
		for len(parts) < f.MinParts {
			parts = append(parts, "0")
		}
		return f.Prefix + strings.Join(parts, "."), nil
	}
	params := make([]string, 6)
	for i, binding := range []*frameworkValue{f.Host, f.Port, f.Scheme, f.User, f.Password, f.Path} {
		value, err := s.text(binding)
		if err != nil {
			return "", err
		}
		params[i] = value
	}
	host, port, scheme, user, password, pathname := params[0], params[1], params[2], params[3], params[4], params[5]
	if host == "" {
		return "", fmt.Errorf("%s formatter requires a nonempty host", f.Kind)
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("%s formatter requires a valid port", f.Kind)
		}
	}
	if f.Kind == "host-port" {
		if port == "" || port == f.OmitPort {
			return host, nil
		}
		return net.JoinHostPort(strings.Trim(host, "[]"), port), nil
	}
	if port != "" {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	} else if strings.Contains(host, ":") {
		host = "[" + strings.Trim(host, "[]") + "]"
	}
	query := url.Values{}
	for key, value := range f.Query {
		text, err := s.text(&value)
		if err != nil {
			return "", err
		}
		if text != "" {
			query.Set(key, text)
		}
	}
	u := url.URL{Scheme: scheme, Host: host, Path: "/" + pathname, RawPath: "/" + url.PathEscape(pathname), RawQuery: query.Encode()}
	if f.User != nil || f.Password != nil {
		u.User = url.UserPassword(user, password)
	}
	if scheme == "" || host == "" {
		return "", fmt.Errorf("url formatter requires nonempty scheme and host")
	}
	return u.String(), nil
}

// orderedFrameworkKeys is shared by dotenv and PHP writers to make insertions
// deterministic while allowing a manifest to choose a familiar field order.
func orderedFrameworkKeys(values map[string]frameworkValue, order []string) []string {
	keys := slices.Clone(order)
	for _, key := range slices.Sorted(maps.Keys(values)) {
		if !slices.Contains(order, key) {
			keys = append(keys, key)
		}
	}
	return keys
}

// frameworkManifestRuntimeEnv applies the block guard and resolves declared variables.
func frameworkManifestRuntimeEnv(ctx RuntimeEnvContext, m frameworkManifest) (map[string]string, error) {
	state := newFrameworkValueState(m, ctx)
	if m.RuntimeEnv.RequiresDatabase && state.database == nil {
		return nil, nil
	}
	result := make(map[string]string, len(m.RuntimeEnv.Values))
	for _, key := range slices.Sorted(maps.Keys(m.RuntimeEnv.Values)) {
		value, err := state.reference("runtime-env." + key)
		if err != nil {
			return nil, fmt.Errorf("framework %q: %w", m.ID, err)
		}
		result[key] = frameworkScalarText(value)
	}
	return result, nil
}
