package tools

import (
	"archive/zip"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// resolveGitHubTagCommit returns the commit SHA underlying a lightweight or
// annotated GitHub tag.
func resolveGitHubTagCommit(client *http.Client, tool string, github manifestGitHubDownload, tag string) (string, error) {
	owner := strings.TrimSpace(github.Owner)
	repo := strings.TrimSpace(github.Repo)
	if owner == "" || repo == "" {
		return "", fmt.Errorf("%s download resolver requires github owner and repo", tool)
	}
	trimmedTag := strings.TrimSpace(tag)
	if trimmedTag == "" {
		return "", fmt.Errorf("%s download resolver requires a resolved tag", tool)
	}

	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", strings.TrimRight(githubAPIBaseURL, "/"), owner, repo, trimmedTag)
	var commit struct {
		SHA string `json:"sha"`
	}
	if err := downloadJSON(client, url, "github tag commit", &commit); err != nil {
		return "", err
	}
	sha := strings.TrimSpace(commit.SHA)
	if sha == "" {
		return "", fmt.Errorf("github tag %q resolved to an empty commit sha", trimmedTag)
	}

	return sha, nil
}

// downloadGitHubSourceArchive downloads a tag source zip, verifies its
// embedded commit, and caches it as a normal archive payload.
func downloadGitHubSourceArchive(client *http.Client, cacheDir, tool, requestedVersion, resolvedVersion string, asset downloadAsset, commit string) error {
	if err := os.MkdirAll(filepath.Join(cacheDir, tool), 0o755); err != nil {
		return fmt.Errorf("create %s cache dir: %w", tool, err)
	}
	stagingDir, err := os.MkdirTemp(filepath.Join(cacheDir, tool), requestedVersion+"-tmp-")
	if err != nil {
		return fmt.Errorf("create %s staging dir: %w", tool, err)
	}
	defer os.RemoveAll(stagingDir)

	archivePath := filepath.Join(stagingDir, asset.FileName)
	if err := downloadFile(client, asset.URL, archivePath); err != nil {
		return err
	}
	if err := verifyGitHubArchiveCommit(tool, archivePath, commit); err != nil {
		return err
	}

	_, err = cacheArchivePayload(cacheDir, tool, requestedVersion, resolvedVersion, asset, archivePath)
	return err
}

// verifyGitHubArchiveCommit checks GitHub's archive comment against the commit
// resolved for the requested tag.
func verifyGitHubArchiveCommit(tool, archivePath, commit string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("open %s archive %s: %w", tool, archivePath, err)
	}
	defer reader.Close()

	comment := strings.TrimSpace(reader.Comment)
	expected := strings.TrimSpace(commit)
	if comment == "" {
		return fmt.Errorf("%s archive %s has no embedded commit to verify against tag commit %s", tool, archivePath, expected)
	}
	if !strings.EqualFold(comment, expected) {
		return fmt.Errorf("%s archive commit %s does not match expected tag commit %s", tool, comment, expected)
	}

	return nil
}
