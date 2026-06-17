package service

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"polka/config"
)

const (
	MailpitListenHost      = "127.0.0.1"
	DefaultMailpitSMTPPort = 1025
	DefaultMailpitUIPort   = 8025
)

func normalizeMailpitConfig(mailpit *config.MailpitConfig) *config.MailpitConfig {
	return config.NormalizeMailpitConfig(mailpit)
}

func validateMailpitConfig(mailpit *config.MailpitConfig) error {
	if mailpit == nil {
		return nil
	}
	if strings.TrimSpace(mailpit.Version) == "" {
		return fmt.Errorf("mailpit configuration requires version")
	}
	if err := validateServiceVersion(toolMailpit, mailpit.Version); err != nil {
		return err
	}
	if mailpit.SMTPPort != 0 && !validPort(mailpit.SMTPPort) {
		return fmt.Errorf("mailpit smtp-port must be between 1 and 65535")
	}
	if mailpit.UIPort != 0 && !validPort(mailpit.UIPort) {
		return fmt.Errorf("mailpit ui-port must be between 1 and 65535")
	}

	return nil
}

func EffectiveMailpitSMTPPort(mailpit *MailpitConfig) int {
	if mailpit == nil || mailpit.SMTPPort == 0 {
		return DefaultMailpitSMTPPort
	}

	return mailpit.SMTPPort
}

func EffectiveMailpitUIPort(mailpit *MailpitConfig) int {
	if mailpit == nil || mailpit.UIPort == 0 {
		return DefaultMailpitUIPort
	}

	return mailpit.UIPort
}

func MailpitAddress(port int) string {
	return net.JoinHostPort(MailpitListenHost, strconv.Itoa(port))
}

func validPort(port int) bool {
	return port >= 1 && port <= 65535
}

var validServiceVersion = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func validateServiceVersion(tool, version string) error {
	trimmed := strings.TrimSpace(version)
	if trimmed == "" {
		return fmt.Errorf("%s version cannot be empty", tool)
	}
	if !validServiceVersion.MatchString(trimmed) {
		return fmt.Errorf("invalid %s version %q: use letters, numbers, dots, dashes, or underscores", tool, version)
	}

	return nil
}
