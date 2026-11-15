package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"polka/backend"
	"polka/config"
	"polka/service"
)

// Dashboard row statuses. "unset" means the service is not configured for the
// environment, matching the wording of the status command.
const (
	dashboardStatusRunning = "running"
	dashboardStatusStopped = "stopped"
	dashboardStatusUnset   = "unset"
)

// dashboardServiceRow is the display state of one managed service.
type dashboardServiceRow struct {
	Name      string
	Status    string
	Detail    string // URL or address summary; empty when not running
	PID       int
	StartedAt time.Time
}

// dashboardSnapshot is one poll of the active environment's runtime state,
// gathered from the same loaders the status command uses and rendered by
// renderDashboard.
type dashboardSnapshot struct {
	EnvironmentName   string
	TakenAt           time.Time
	NoEnvironment     bool
	WebServer         *serveRuntimeState
	Services          []dashboardServiceRow
	Workers           *workersRuntimeState
	WorkersConfigured int                      // total configured replicas
	WorkersUnset      bool                     // no workers configured at all
	Cron              *service.CronRunnerState // nil when the runner is stopped
	CronJobs          []dashboardCronRow
	CronUnset         bool // no cron jobs configured at all
	Errors            []string
}

// dashboardCronRow is the display state of one configured cron job.
type dashboardCronRow struct {
	Name     string
	Schedule string
	Next     time.Time // zero when the schedule cannot be evaluated
	Last     service.CronJobRunState
}

// recordError keeps a loader failure visible in the dashboard footer instead
// of aborting the whole snapshot; a transient probe failure must not kill the
// live view.
func (s *dashboardSnapshot) recordError(name string, err error) {
	s.Errors = append(s.Errors, fmt.Sprintf("%s: %v", name, err))
}

// gatherDashboardSnapshot polls the live runtime state for the active
// environment. The environment is re-read on every call so config changes
// show up while the dashboard is open. The state loaders already clean up
// stale entries, making this safe to call on a short interval.
func gatherDashboardSnapshot(store backend.Store, now time.Time) dashboardSnapshot {
	snapshot := dashboardSnapshot{TakenAt: now}

	current, err := store.Current()
	if err != nil {
		snapshot.recordError("environment", err)
		return snapshot
	}
	if current == nil {
		snapshot.NoEnvironment = true
		return snapshot
	}
	// Rows report effective ports, including leased ones.
	environment, err := runtimeServiceEnvironment(store, *current)
	if err != nil {
		snapshot.recordError("ports", err)
		environment = *current
	}
	snapshot.EnvironmentName = environment.Name

	webServerState, err := loadWebServerState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError("webserver", err)
	} else {
		snapshot.WebServer = webServerState
	}

	snapshot.Services = []dashboardServiceRow{
		gatherDatabaseRow(store, environment, &snapshot),
		gatherMailpitRow(store, environment, &snapshot),
		gatherPHPMyAdminRow(store, environment, &snapshot),
		gatherMeilisearchRow(store, environment, &snapshot),
		gatherRedisRow(store, environment, &snapshot),
		gatherRabbitMQRow(store, environment, &snapshot),
		gatherRouterRow(store, environment, &snapshot),
	}

	gatherDashboardWorkers(store, environment, &snapshot)
	gatherDashboardCron(store, environment, &snapshot)

	return snapshot
}

// gatherDashboardWorkers fills the workers section of the snapshot.
func gatherDashboardWorkers(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) {
	if len(environment.Workers) == 0 {
		snapshot.WorkersUnset = true
		return
	}
	for _, worker := range environment.Workers {
		snapshot.WorkersConfigured += config.EffectiveWorkerReplicas(worker)
	}
	workersState, err := loadLiveWorkersState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError("workers", err)
		return
	}
	snapshot.Workers = workersState
}

// gatherDashboardCron fills the cron section of the snapshot: the runner's
// state and one row per configured job with its next run and last result.
func gatherDashboardCron(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) {
	if len(environment.Cron) == 0 {
		snapshot.CronUnset = true
		return
	}

	runner, err := loadLiveCronState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError("cron", err)
	} else {
		snapshot.Cron = runner
	}
	history, err := service.LoadCronRunsState(service.CronRunsPath(store.RootDir, environment.Name))
	if err != nil {
		snapshot.recordError("cron history", err)
	}

	for _, name := range config.SortedCronJobNames(environment.Cron) {
		job := environment.Cron[name]
		row := dashboardCronRow{Name: name, Schedule: job.Schedule, Last: history.Jobs[name]}
		if schedule, err := config.ParseCronSchedule(job.Schedule); err == nil {
			row.Next = schedule.Next(snapshot.TakenAt)
		}
		snapshot.CronJobs = append(snapshot.CronJobs, row)
	}
}

func gatherDatabaseRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "database", Status: dashboardStatusUnset}
	if environment.Database == nil || strings.TrimSpace(environment.Database.Engine) == "" {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := service.LoadLiveManagedDatabaseState(store.RootDir, environment.Name, pingDatabaseAddressFunc)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.Detail = fmt.Sprintf("%s:%s@%d", state.Engine, state.Version, state.Port)
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	return row
}

func gatherMailpitRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "mailpit", Status: dashboardStatusUnset}
	if strings.TrimSpace(environment.ToolVersion("mailpit")) == "" {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := loadLiveMailpitState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.Detail = fmt.Sprintf("smtp=%d ui=%s", state.SMTPPort, mailpitUIURL(*state))
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	return row
}

func gatherPHPMyAdminRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "phpmyadmin", Status: dashboardStatusUnset}
	if strings.TrimSpace(environment.ToolVersion("phpmyadmin")) == "" {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := loadLivePHPMyAdminState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.Detail = serveStateURL(*state)
	row.PID = state.PrimaryPID
	row.StartedAt = state.StartedAt
	return row
}

func gatherMeilisearchRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "meilisearch", Status: dashboardStatusUnset}
	if strings.TrimSpace(environment.ToolVersion("meilisearch")) == "" {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := loadLiveMeilisearchState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.Detail = meilisearchURL(*state)
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	return row
}

func gatherRedisRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "redis", Status: dashboardStatusUnset}
	if strings.TrimSpace(environment.ToolVersion("redis")) == "" {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := loadLiveRedisState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.Detail = redisURL(*state)
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	return row
}

func gatherRabbitMQRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "rabbitmq", Status: dashboardStatusUnset}
	rabbitMQ := environment.RabbitMQ()
	if rabbitMQ == nil || strings.TrimSpace(rabbitMQ.Version) == "" {
		return row
	}
	row.Status = dashboardStatusStopped
	state, err := loadLiveRabbitMQState(store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}
	row.Status = dashboardStatusRunning
	row.Detail = fmt.Sprintf("amqp=%s ui=%s", rabbitMQURLForConfig(rabbitMQ), rabbitMQManagementURLForConfig(rabbitMQ))
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	return row
}

// gatherRouterRow reports the shared global router and, when this
// environment is routed through it, the environment's public URL.
func gatherRouterRow(store backend.Store, environment backend.Environment, snapshot *dashboardSnapshot) dashboardServiceRow {
	row := dashboardServiceRow{Name: "router", Status: dashboardStatusUnset}
	dir, ok := environmentRouterDir(store, environment)
	if !ok {
		return row
	}

	row.Status = dashboardStatusStopped
	state, err := service.LoadLiveRouterState(dir, routerRuntimeHooks())
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if state == nil {
		return row
	}

	row.Status = dashboardStatusRunning
	row.PID = state.PID
	row.StartedAt = state.StartedAt
	route, err := service.LookupRoute(dir, store.RootDir, environment.Name)
	if err != nil {
		snapshot.recordError(row.Name, err)
		return row
	}
	if route != nil {
		row.Detail = service.PublicURL(route.Host, route.HTTPS, routerSettingsFromState(*state))
	}
	return row
}

// Dashboard styles. AdaptiveColor keeps the palette readable on both light
// and dark terminal backgrounds.
var (
	dashboardHeaderStyle  = lipgloss.NewStyle().Bold(true)
	dashboardSectionStyle = lipgloss.NewStyle().Bold(true)
	dashboardDimStyle     = lipgloss.NewStyle().Faint(true)
	dashboardRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "28", Dark: "42"})
	dashboardStoppedStyle = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "130", Dark: "214"})
	dashboardErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"})
)

// dashboardStatusCell pads the status to a fixed column width before styling,
// because styling first would make the escape codes count toward the padding.
func dashboardStatusCell(status string) string {
	padded := fmt.Sprintf("%-8s", status)
	switch status {
	case dashboardStatusRunning:
		return dashboardRunningStyle.Render(padded)
	case dashboardStatusStopped:
		return dashboardStoppedStyle.Render(padded)
	default:
		return dashboardDimStyle.Render(padded)
	}
}

// renderDashboard renders one snapshot as the full-screen dashboard view.
func renderDashboard(snapshot dashboardSnapshot) string {
	var b strings.Builder

	if snapshot.NoEnvironment {
		b.WriteString(dashboardHeaderStyle.Render("polka dashboard"))
		b.WriteString("\n\nNo active environment selected.\n\n")
		b.WriteString(dashboardDimStyle.Render("q quit"))
		b.WriteString("\n")
		return b.String()
	}

	header := fmt.Sprintf("polka dashboard — environment %s", snapshot.EnvironmentName)
	b.WriteString(dashboardHeaderStyle.Render(header))
	b.WriteString(dashboardDimStyle.Render(fmt.Sprintf("   updated %s", snapshot.TakenAt.Format("15:04:05"))))
	b.WriteString("\n\n")

	b.WriteString(dashboardSectionStyle.Render("Webserver"))
	b.WriteString("\n")
	if snapshot.WebServer == nil {
		b.WriteString(fmt.Sprintf("  %-12s %s\n", "", dashboardStatusCell(dashboardStatusStopped)))
	} else {
		state := *snapshot.WebServer
		b.WriteString(fmt.Sprintf("  %-12s %s %s  pid %d  up %s\n",
			serveRuntimeLabel(state.ServerKind),
			dashboardStatusCell(dashboardStatusRunning),
			serveStateURL(state),
			state.PrimaryPID,
			formatUptime(snapshot.TakenAt, state.StartedAt)))
	}
	b.WriteString("\n")

	b.WriteString(dashboardSectionStyle.Render("Services"))
	b.WriteString("\n")
	for _, row := range snapshot.Services {
		b.WriteString(fmt.Sprintf("  %-12s %s", row.Name, dashboardStatusCell(row.Status)))
		if row.Status == dashboardStatusRunning {
			b.WriteString(fmt.Sprintf(" %s  pid %d  up %s", row.Detail, row.PID, formatUptime(snapshot.TakenAt, row.StartedAt)))
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	renderDashboardWorkers(&b, snapshot)
	renderDashboardCron(&b, snapshot)

	for _, message := range snapshot.Errors {
		b.WriteString(dashboardErrorStyle.Render(fmt.Sprintf("error: %s", message)))
		b.WriteString("\n")
	}
	if len(snapshot.Errors) > 0 {
		b.WriteString("\n")
	}

	b.WriteString(dashboardDimStyle.Render(fmt.Sprintf("refreshes every %s — q quit", dashboardPollInterval)))
	b.WriteString("\n")

	return b.String()
}

// renderDashboardWorkers renders the workers section: a summary line plus one
// row per live worker process.
func renderDashboardWorkers(b *strings.Builder, snapshot dashboardSnapshot) {
	if snapshot.WorkersUnset {
		b.WriteString(dashboardSectionStyle.Render("Workers"))
		b.WriteString("\n  ")
		b.WriteString(dashboardDimStyle.Render(dashboardStatusUnset))
		b.WriteString("\n\n")
		return
	}

	if snapshot.Workers == nil || len(snapshot.Workers.Processes) == 0 {
		b.WriteString(dashboardSectionStyle.Render(fmt.Sprintf("Workers (0/%d)", snapshot.WorkersConfigured)))
		b.WriteString("\n  ")
		b.WriteString(dashboardStoppedStyle.Render(dashboardStatusStopped))
		b.WriteString("\n\n")
		return
	}

	state := *snapshot.Workers
	b.WriteString(dashboardSectionStyle.Render(fmt.Sprintf("Workers (%d/%d: %s)", len(state.Processes), snapshot.WorkersConfigured, workerProcessSummary(state))))
	b.WriteString("\n")
	for _, process := range state.Processes {
		b.WriteString(fmt.Sprintf("  %-12s %s  pid %d  up %s\n",
			fmt.Sprintf("%s #%d", process.Name, process.Replica),
			dashboardStatusCell(dashboardStatusRunning),
			process.PID,
			formatUptime(snapshot.TakenAt, process.StartedAt)))
	}
	b.WriteString("\n")
}

// renderDashboardCron renders the cron section: the runner's state plus one
// row per configured job with its schedule, next run, and last result.
func renderDashboardCron(b *strings.Builder, snapshot dashboardSnapshot) {
	if snapshot.CronUnset {
		b.WriteString(dashboardSectionStyle.Render("Cron"))
		b.WriteString("\n  ")
		b.WriteString(dashboardDimStyle.Render(dashboardStatusUnset))
		b.WriteString("\n\n")
		return
	}

	if snapshot.Cron == nil {
		b.WriteString(dashboardSectionStyle.Render("Cron"))
		b.WriteString(" ")
		b.WriteString(dashboardStoppedStyle.Render(dashboardStatusStopped))
	} else {
		b.WriteString(dashboardSectionStyle.Render("Cron"))
		b.WriteString(" ")
		b.WriteString(dashboardRunningStyle.Render(dashboardStatusRunning))
		b.WriteString(fmt.Sprintf("  pid %d  up %s", snapshot.Cron.PID, formatUptime(snapshot.TakenAt, snapshot.Cron.StartedAt)))
	}
	b.WriteString("\n")
	for _, row := range snapshot.CronJobs {
		next := "-"
		if !row.Next.IsZero() {
			next = row.Next.Format("01-02 15:04")
		}
		b.WriteString(fmt.Sprintf("  %-12s %-14s next %s  last %s\n", row.Name, row.Schedule, next, dashboardCronLastRun(row.Last)))
	}
	b.WriteString("\n")
}

// dashboardCronLastRun renders a job's latest result compactly.
func dashboardCronLastRun(run service.CronJobRunState) string {
	switch {
	case run.Running:
		return run.LastStart.Local().Format("01-02 15:04") + " running"
	case run.LastStart.IsZero():
		return "never"
	case run.LastError != "":
		return run.LastStart.Local().Format("01-02 15:04") + " failed to start"
	default:
		return fmt.Sprintf("%s exit %d", run.LastStart.Local().Format("01-02 15:04"), run.LastExitCode)
	}
}

// formatUptime renders the elapsed time since startedAt in a compact form
// ("42s", "12m", "2h13m", "3d2h"). A zero or future start time renders as "-".
func formatUptime(now, startedAt time.Time) string {
	if startedAt.IsZero() || now.Before(startedAt) {
		return "-"
	}

	elapsed := now.Sub(startedAt)
	switch {
	case elapsed < time.Minute:
		return fmt.Sprintf("%ds", int(elapsed.Seconds()))
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(elapsed.Hours()), int(elapsed.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(elapsed.Hours())/24, int(elapsed.Hours())%24)
	}
}
