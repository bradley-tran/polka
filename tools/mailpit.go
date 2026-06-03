package tools

import (
	"fmt"
	"net/http"
	"strings"

	"polka/config"
)

func mailpitPlugin() Plugin {
	return newManifestPlugin(Mailpit, pluginHooks{
		validate: func(environment config.Environment) error {
			return validateMailpitConfig(environment.Mailpit)
		},
	})
}

func validateMailpitConfig(mailpit *config.MailpitConfig) error {
	if mailpit == nil {
		return nil
	}
	if strings.TrimSpace(mailpit.Version) == "" {
		return fmt.Errorf("mailpit configuration requires version")
	}
	if err := validateVersion(Mailpit, mailpit.Version); err != nil {
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

func downloadMailpit(client *http.Client, cacheDir, version string) error {
	return downloadBuiltinManifestTool(client, cacheDir, Mailpit, version)
}

func resolveMailpitDownloadAsset(requestedVersion, goos, goarch string) (string, databaseDownloadAsset, error) {
	return resolveBuiltinManifestDownloadAsset(Mailpit, requestedVersion, goos, goarch)
}
