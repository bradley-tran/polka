package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/huh"

	"polka/backend"
	"polka/config"
)

// Form group titles. Fields are grouped into huh pages in this order.
const (
	configFormGroupGeneral       = "General"
	configFormGroupPHPRuntime    = "PHP runtime"
	configFormGroupWebServer     = "Web server"
	configFormGroupDatabase      = "Database"
	configFormGroupServices      = "Services"
	configFormGroupEnvVars       = "Environment variables"
	configFormGroupPHPExtensions = "PHP extensions"
	configFormGroupOPcache       = "OPcache"
)

// configFormDynamicGroupNote explains why dynamic-map groups only show
// existing entries.
const configFormDynamicGroupNote = "Existing entries only; add new ones with 'polka config <key> <value>'."

// configFieldKind selects the widget and value handling for a form field.
type configFieldKind int

const (
	// configFieldInput is a free-form text input.
	configFieldInput configFieldKind = iota
	// configFieldSelect offers a fixed option list.
	configFieldSelect
	// configFieldConfirm is a boolean toggle stored as "true"/"false".
	configFieldConfirm
	// configFieldPort is a text input validated as a TCP port; an emptied
	// field is submitted as "0" (unset).
	configFieldPort
)

// configFormField describes one editable config value in the interactive
// settings form. key is the dotted path accepted by Store.ConfigureValue;
// initial holds the environment's current value rendered as a string and
// value receives the (possibly edited) result after the form runs.
type configFormField struct {
	key     string
	title   string
	desc    string
	group   string
	kind    configFieldKind
	options []string // configFieldSelect only; "" represents unset
	initial string
	value   string
}

// configChange is one key/value pair to hand to Store.ConfigureValue.
type configChange struct {
	key   string
	value string
}

// newConfigFormField seeds a field spec with matching initial and current values.
func newConfigFormField(group, key, title, desc string, kind configFieldKind, initial string, options []string) *configFormField {
	return &configFormField{
		key:     key,
		title:   title,
		desc:    desc,
		group:   group,
		kind:    kind,
		options: options,
		initial: initial,
		value:   initial,
	}
}

func configInputField(group, key, title, desc, initial string) *configFormField {
	return newConfigFormField(group, key, title, desc, configFieldInput, initial, nil)
}

func configSelectField(group, key, title, desc, initial string, options []string) *configFormField {
	return newConfigFormField(group, key, title, desc, configFieldSelect, initial, options)
}

func configConfirmField(group, key, title, desc string, initial bool) *configFormField {
	return newConfigFormField(group, key, title, desc, configFieldConfirm, strconv.FormatBool(initial), nil)
}

func configPortField(group, key, title, desc string, port int) *configFormField {
	return newConfigFormField(group, key, title, desc, configFieldPort, configFormPortString(port), nil)
}

// configFormPortString renders a configured port for editing; 0 means unset
// and is shown as an empty field.
func configFormPortString(port int) string {
	if port == 0 {
		return ""
	}

	return strconv.Itoa(port)
}

// validateConfigFormPort accepts an empty value (unset) or an integer that is
// 0 or a valid TCP port, mirroring the backend's parseConfigPortValue.
func validateConfigFormPort(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	port, err := strconv.Atoi(trimmed)
	if err != nil {
		return fmt.Errorf("must be an integer")
	}
	if port != 0 && (port < 1 || port > 65535) {
		return fmt.Errorf("must be 0 or between 1 and 65535")
	}

	return nil
}

// buildConfigFormFields returns the ordered field specs for the interactive
// settings form, pre-filled from the environment. The keys mirror the catalog
// accepted by Store.ConfigureValue. Dynamic maps (env vars, PHP extensions,
// OPcache directives) contribute one field per existing entry; PIE and PECL
// extensions are excluded because they are owned by 'polka ext'.
func buildConfigFormFields(environment backend.Environment) []*configFormField {
	var server config.ServerConfig
	if environment.Server != nil {
		server = *environment.Server
	}
	var database config.DatabaseConfig
	if environment.Database != nil {
		database = *environment.Database
	}
	var mailpit config.MailpitConfig
	if environment.Mailpit != nil {
		mailpit = *environment.Mailpit
	}
	var phpMyAdmin config.PHPMyAdminConfig
	if environment.PHPMyAdmin != nil {
		phpMyAdmin = *environment.PHPMyAdmin
	}
	var meilisearch config.MeilisearchConfig
	if environment.Meilisearch != nil {
		meilisearch = *environment.Meilisearch
	}
	var redis config.RedisConfig
	if environment.Redis != nil {
		redis = *environment.Redis
	}
	var rabbitMQ config.RabbitMQConfig
	if environment.RabbitMQ != nil {
		rabbitMQ = *environment.RabbitMQ
	}
	var traefik config.TraefikConfig
	if environment.Traefik != nil {
		traefik = *environment.Traefik
	}

	fields := []*configFormField{
		configInputField(configFormGroupGeneral, "framework", "Framework", "", environment.Framework),
		configInputField(configFormGroupGeneral, "docroot", "Docroot", "", environment.Docroot),
		configConfirmField(configFormGroupGeneral, "https", "HTTPS", "", environment.HTTPS),
		configInputField(configFormGroupGeneral, "env-file", "Env file", "", environment.EnvFile),
		configInputField(configFormGroupGeneral, "memory-limit", "PHP memory limit", "e.g. 256M; empty for the default", environment.MemoryLimit),
		configSelectField(configFormGroupGeneral, "opcache-preset", "OPcache preset", "", environment.OPcachePreset, []string{"", config.OPcachePresetNone, config.OPcachePresetDev, config.OPcachePresetProduction}),

		configInputField(configFormGroupPHPRuntime, "tools.php", "PHP version", "mutually exclusive with PHP ZTS", environment.PHPVersion),
		configInputField(configFormGroupPHPRuntime, "tools.php-zts", "PHP ZTS version", "mutually exclusive with PHP", environment.PHPZTSVersion),
		configInputField(configFormGroupPHPRuntime, "tools.frankenphp", "FrankenPHP version", "", environment.FrankenPHPVersion),
		configInputField(configFormGroupPHPRuntime, "tools.roadrunner", "RoadRunner version", "", environment.RoadRunnerVersion),
		configInputField(configFormGroupPHPRuntime, "tools.composer", "Composer version", "", environment.ComposerVersion),
		configInputField(configFormGroupPHPRuntime, "tools.nodejs", "Node.js version", "", environment.NodeJSVersion),
		configInputField(configFormGroupPHPRuntime, "tools.mago", "Mago version", "", environment.MagoVersion),

		configInputField(configFormGroupWebServer, "tools.nginx", "nginx version", "", environment.NginxVersion),
		configInputField(configFormGroupWebServer, "tools.apache", "Apache version", "", environment.ApacheVersion),
		configSelectField(configFormGroupWebServer, "server.type", "Server type", "", serverTypeValue(server.Type), []string{"", config.ServerTypePHP, config.ServerTypeNginx, config.ServerTypeApache, config.ServerTypeFrankenPHP}),
		configInputField(configFormGroupWebServer, "server.hostname", "Server hostname", "", server.Hostname),
		configPortField(configFormGroupWebServer, "server.port", "Server port", "", server.Port),

		configInputField(configFormGroupDatabase, "tools.mysql", "MySQL version", "", environment.MySQLVersion),
		configInputField(configFormGroupDatabase, "tools.mariadb", "MariaDB version", "", environment.MariaDBVersion),
		configInputField(configFormGroupDatabase, "tools.postgresql", "PostgreSQL version", "", environment.PostgreSQLVersion),
		configInputField(configFormGroupDatabase, "tools.sqlite", "SQLite version", "", environment.SQLiteVersion),
		configSelectField(configFormGroupDatabase, "database.engine", "Database engine", "", database.Engine, []string{"", "mysql", "mariadb", "postgresql"}),
		configPortField(configFormGroupDatabase, "database.port", "Database port", "", database.Port),

		configInputField(configFormGroupServices, "tools.mailpit", "Mailpit version", "", mailpit.Version),
		configPortField(configFormGroupServices, "settings.mailpit.smtp-port", "Mailpit SMTP port", "", mailpit.SMTPPort),
		configPortField(configFormGroupServices, "settings.mailpit.ui-port", "Mailpit UI port", "", mailpit.UIPort),
		configInputField(configFormGroupServices, "tools.phpmyadmin", "phpMyAdmin version", "", phpMyAdmin.Version),
		configPortField(configFormGroupServices, "settings.phpmyadmin.port", "phpMyAdmin port", "", phpMyAdmin.Port),
		configInputField(configFormGroupServices, "tools.meilisearch", "Meilisearch version", "", meilisearch.Version),
		configPortField(configFormGroupServices, "settings.meilisearch.port", "Meilisearch port", "", meilisearch.Port),
		configInputField(configFormGroupServices, "settings.meilisearch.master-key", "Meilisearch master key", "", meilisearch.MasterKey),
		configInputField(configFormGroupServices, "tools.redis", "Redis version", "", redis.Version),
		configPortField(configFormGroupServices, "settings.redis.port", "Redis port", "", redis.Port),
		configInputField(configFormGroupServices, "settings.redis.password", "Redis password", "", redis.Password),
		configInputField(configFormGroupServices, "tools.rabbitmq", "RabbitMQ version", "Windows amd64 only", rabbitMQ.Version),
		configPortField(configFormGroupServices, "settings.rabbitmq.port", "RabbitMQ AMQP port", "", rabbitMQ.Port),
		configPortField(configFormGroupServices, "settings.rabbitmq.management-port", "RabbitMQ management port", "", rabbitMQ.ManagementPort),
		configInputField(configFormGroupServices, "settings.rabbitmq.username", "RabbitMQ username", "defaults to guest", rabbitMQ.Username),
		configInputField(configFormGroupServices, "settings.rabbitmq.password", "RabbitMQ password", "defaults to guest", rabbitMQ.Password),
		configInputField(configFormGroupServices, "tools.traefik", "Traefik version", "", traefik.Version),
		configPortField(configFormGroupServices, "settings.traefik.port", "Traefik port", "", traefik.Port),
	}

	for _, name := range sortedMapKeys(environment.EnvVars) {
		fields = append(fields, configInputField(configFormGroupEnvVars, "env-vars."+name, name, "", environment.EnvVars[name]))
	}
	for _, name := range sortedMapKeys(environment.PHPExtensions) {
		fields = append(fields, configConfirmField(configFormGroupPHPExtensions, "php-extensions."+name, name, "", environment.PHPExtensions[name]))
	}
	for _, directive := range sortedMapKeys(environment.OPcacheConfig) {
		fields = append(fields, configInputField(configFormGroupOPcache, "opcache-config."+directive, directive, "", environment.OPcacheConfig[directive]))
	}

	return fields
}

// serverTypeValue normalizes a stored server type so the Select pre-highlights
// the matching option.
func serverTypeValue(serverType string) string {
	return strings.ToLower(strings.TrimSpace(serverType))
}

// sortedMapKeys returns the map's keys in stable order for deterministic form
// field placement.
func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	return keys
}

// configFormChanges returns the edited fields as ConfigureValue key/value
// pairs in field-declaration order, so tool versions apply before the
// server/database/settings values that are validated against them.
func configFormChanges(fields []*configFormField) []configChange {
	var changes []configChange
	for _, field := range fields {
		if field.value == field.initial {
			continue
		}
		value := field.value
		// The backend's parseConfigPortValue rejects empty input; an emptied
		// port field means "unset", which is stored as port 0.
		if field.kind == configFieldPort && strings.TrimSpace(value) == "" {
			value = "0"
		}
		changes = append(changes, configChange{key: field.key, value: value})
	}

	return changes
}

// applyConfigFormChanges applies each change through Store.ConfigureValue,
// printing the same confirmation line as 'polka config <key> <value>'. A
// failing change is reported on stderr and the remaining changes still apply,
// since form edits are independent of each other. It returns the number of
// changes applied and a statusError when any change failed.
func applyConfigFormChanges(stdout, stderr io.Writer, store backend.Store, name string, changes []configChange) (int, error) {
	if len(changes) == 0 {
		_, _ = fmt.Fprintln(stdout, "No changes.")
		return 0, nil
	}

	applied := 0
	var lastEnvironment *backend.Environment
	for _, change := range changes {
		environment, err := store.ConfigureValue(name, change.key, change.value)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %s: %v\n", change.key, err)
			continue
		}
		applied++
		lastEnvironment = &environment
		_, _ = fmt.Fprintf(stdout, "Configured %s\t%s=%s\n", environment.Name, change.key, change.value)
	}

	// Emit the standard configuration warnings once for the final state
	// instead of repeating them after every applied change.
	if lastEnvironment != nil {
		writePHPCLIRuntimeWarning(stderr, *lastEnvironment)
		writePHPMyAdminPostgreSQLWarning(stderr, *lastEnvironment)
	}

	_, _ = fmt.Fprintf(stdout, "Applied %d of %d change(s).\n", applied, len(changes))
	if applied < len(changes) {
		return applied, &statusError{code: 1, err: fmt.Errorf("%d of %d changes failed", len(changes)-applied, len(changes))}
	}

	return applied, nil
}

// newConfigForm builds the huh form for the field specs and returns it with a
// finalize callback that must run after the form completes to copy
// widget-bound values (the Confirm booleans) back into the specs.
func newConfigForm(fields []*configFormField) (*huh.Form, func()) {
	groupOrder := make([]string, 0)
	groupFields := map[string][]huh.Field{}
	var finalizers []func()

	for _, field := range fields {
		var widget huh.Field
		switch field.kind {
		case configFieldSelect:
			widget = huh.NewSelect[string]().
				Title(field.title).
				Description(field.desc).
				Options(configSelectOptions(field.options)...).
				Value(&field.value)
		case configFieldConfirm:
			// Confirm binds a bool, so seed it from the field's string value
			// and copy the result back once the form is done.
			enabled := field.value == "true"
			bound := field
			finalizers = append(finalizers, func() {
				bound.value = strconv.FormatBool(enabled)
			})
			widget = huh.NewConfirm().
				Title(field.title).
				Description(field.desc).
				Value(&enabled)
		case configFieldPort:
			widget = huh.NewInput().
				Title(field.title).
				Description(field.desc).
				Validate(validateConfigFormPort).
				Value(&field.value)
		default:
			widget = huh.NewInput().
				Title(field.title).
				Description(field.desc).
				Value(&field.value)
		}

		if _, seen := groupFields[field.group]; !seen {
			groupOrder = append(groupOrder, field.group)
		}
		groupFields[field.group] = append(groupFields[field.group], widget)
	}

	groups := make([]*huh.Group, 0, len(groupOrder))
	for _, name := range groupOrder {
		group := huh.NewGroup(groupFields[name]...).Title(name)
		switch name {
		case configFormGroupEnvVars, configFormGroupPHPExtensions, configFormGroupOPcache:
			group = group.Description(configFormDynamicGroupNote)
		}
		groups = append(groups, group)
	}

	finalize := func() {
		for _, fn := range finalizers {
			fn()
		}
	}

	return huh.NewForm(groups...), finalize
}

// configSelectOptions renders select options, labelling the empty value as
// "(unset)" so it is visible in the option list.
func configSelectOptions(values []string) []huh.Option[string] {
	options := make([]huh.Option[string], 0, len(values))
	for _, value := range values {
		label := value
		if value == "" {
			label = "(unset)"
		}
		options = append(options, huh.NewOption(label, value))
	}

	return options
}

// runConfigForm opens the interactive settings form for an environment and
// applies the edited values on submit. It backs 'polka config' when invoked
// without a key and value.
func runConfigForm(stdout, stderr io.Writer, store backend.Store, name string) error {
	if !isTTY(stdout) || !stdinIsTTY() {
		return &statusError{code: 1, err: fmt.Errorf("interactive config requires a terminal; run 'polka config <key> <value>' instead")}
	}

	resolvedName, setCurrent, err := resolveCommandEnvironmentName(stdout, store, name, "Configuring")
	if err != nil {
		return err
	}

	// Pre-fill from the stored environment when it exists; a not-yet-created
	// environment starts from a blank form and is created on first apply.
	var environment backend.Environment
	environments, err := store.List()
	if err != nil {
		return err
	}
	for _, candidate := range environments {
		if candidate.Name == resolvedName {
			environment = candidate
			break
		}
	}

	fields := buildConfigFormFields(environment)
	form, finalize := newConfigForm(fields)
	if err := form.WithInput(os.Stdin).WithOutput(stdout).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			_, _ = fmt.Fprintln(stdout, "Cancelled; no changes applied.")
			return nil
		}

		return &statusError{code: 1, err: err}
	}
	finalize()

	applied, applyErr := applyConfigFormChanges(stdout, stderr, store, resolvedName, configFormChanges(fields))
	// Mirror runConfig: select the fallback environment once it exists, which
	// requires at least one applied change to have created it.
	if setCurrent && applied > 0 {
		if err := store.Use(resolvedName); err != nil {
			return err
		}
	}

	return applyErr
}
