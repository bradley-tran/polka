package tools

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// serveBuildFixture serves fixed bodies by path and fails the test on any other
// request, so a script cannot silently depend on an unexpected URL.
func serveBuildFixture(t *testing.T, pages map[string]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := pages[r.URL.Path]
		if !ok {
			t.Errorf("unexpected request for %s", r.URL.String())
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, body)
	}))
	t.Cleanup(server.Close)

	return server
}

// loadBuildManifestWithVars loads a built-in manifest with its upstream hosts
// pointed at a test server.
func loadBuildManifestWithVars(t *testing.T, tool string, vars map[string]string) pluginManifest {
	t.Helper()
	manifest, err := loadBuiltinManifest(tool)
	if err != nil {
		t.Fatalf("loadBuiltinManifest(%s) error = %v", tool, err)
	}
	merged := map[string]string{}
	for key, value := range manifest.Script.Vars {
		merged[key] = value
	}
	for key, value := range vars {
		merged[key] = value
	}
	manifest.Script.Vars = merged

	return manifest
}

// TestPHPBuildSources verifies the php.net index resolves to a tar.gz with its
// published checksum, for both flavors, without touching the Windows resolve path.
func TestPHPBuildSources(t *testing.T) {
	checksum := phpTestChecksum("php-8.4.27")
	release := fmt.Sprintf(`{"version":"8.4.27","source":[
		{"filename":"php-8.4.27.tar.bz2","sha256":"%[1]s"},
		{"filename":"php-8.4.27.tar.gz","sha256":"%[1]s"}]}`, checksum)
	server := serveBuildFixture(t, map[string]string{"/releases": release})

	for _, tool := range []string{PHP, PHPZTS} {
		manifest := loadBuildManifestWithVars(t, tool, map[string]string{
			"source-releases": server.URL + "/releases",
			"source-base":     "https://example.test/distributions/",
		})
		plan, err := resolveBuildSources(server.Client(), manifest, "8.4", "linux", "amd64")
		if err != nil {
			t.Fatalf("%s resolveBuildSources() error = %v", tool, err)
		}
		if plan.Version != "8.4.27" {
			t.Fatalf("%s version = %q, want 8.4.27", tool, plan.Version)
		}
		source := plan.Sources["php"]
		if source.URL != "https://example.test/distributions/php-8.4.27.tar.gz" || source.Checksum != checksum || source.ChecksumAlgorithm != checksumAlgorithmSHA256 || source.ArchiveFormat != archiveFormatTarGz {
			t.Fatalf("%s source = %+v", tool, source)
		}
	}
}

// TestPHPBuildSourcesRejectsBadReleases verifies an unknown version, a release
// that does not match the request, and a version below the recipe's minimum.
func TestPHPBuildSourcesRejectsBadReleases(t *testing.T) {
	sourceList := `"source":[{"filename":"php-%[1]s.tar.gz","sha256":"` + phpTestChecksum("x") + `"}]`
	tests := []struct {
		name      string
		requested string
		release   string
		want      string
	}{
		{name: "unknown version", requested: "9.9", release: `{"error":"Unknown version"}`, want: "no matching source release"},
		{name: "wrong release", requested: "8.4", release: fmt.Sprintf(`{"version":"8.3.1",`+sourceList+`}`, "8.3.1"), want: "php.net returned 8.3.1"},
		{name: "below minimum", requested: "7.4", release: fmt.Sprintf(`{"version":"7.4.33",`+sourceList+`}`, "7.4.33"), want: "requires 8.2 or newer"},
		{name: "no tar.gz", requested: "8.4", release: `{"version":"8.4.1","source":[{"filename":"php-8.4.1.tar.bz2","sha256":"x"}]}`, want: "lists no tar.gz"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := serveBuildFixture(t, map[string]string{"/releases": test.release})
			manifest := loadBuildManifestWithVars(t, PHP, map[string]string{"source-releases": server.URL + "/releases"})
			_, err := resolveBuildSources(server.Client(), manifest, test.requested, "linux", "amd64")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolveBuildSources() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestRedisBuildSources verifies the hash list resolves the newest matching stable
// release over HTTPS, ignoring release candidates and the list's plain-HTTP link.
func TestRedisBuildSources(t *testing.T) {
	stable := phpTestChecksum("stable")
	hashes := strings.Join([]string{
		"hash redis-8.2.9.tar.gz sha256 " + phpTestChecksum("old") + " http://download.redis.io/releases/redis-8.2.9.tar.gz",
		"hash redis-8.2.11.tar.gz sha256 " + stable + " http://download.redis.io/releases/redis-8.2.11.tar.gz",
		"hash redis-8.12-rc1.tar.gz sha256 " + phpTestChecksum("rc") + " http://download.redis.io/releases/redis-8.12-rc1.tar.gz",
	}, "\n")
	server := serveBuildFixture(t, map[string]string{"/hashes": hashes})
	manifest := loadBuildManifestWithVars(t, Redis, map[string]string{"source-hashes": server.URL + "/hashes"})

	plan, err := resolveBuildSources(server.Client(), manifest, "8.2", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveBuildSources() error = %v", err)
	}
	source := plan.Sources["redis"]
	if plan.Version != "8.2.11" || source.Checksum != stable {
		t.Fatalf("plan = %+v", plan)
	}
	if source.URL != "https://download.redis.io/releases/redis-8.2.11.tar.gz" {
		t.Fatalf("source URL = %q, want HTTPS", source.URL)
	}

	if _, err := resolveBuildSources(server.Client(), manifest, "8.12", "linux", "amd64"); err == nil || !strings.Contains(err.Error(), "no matching source release") {
		t.Fatalf("resolveBuildSources(rc only) error = %v, want no match", err)
	}
}

// TestNginxBuildSourcesIsExplicitlyUnverified verifies nginx, which publishes no
// checksums, declares that rather than quietly omitting one, and resolves tar.gz
// sources while the listing also holds zips and signatures.
func TestNginxBuildSourcesIsExplicitlyUnverified(t *testing.T) {
	listing := `<a href="nginx-1.30.5.zip">a</a><a href="nginx-1.30.6.tar.gz">b</a><a href="nginx-1.30.6.tar.gz.asc">c</a><a href="nginx-1.29.9.tar.gz">d</a>`
	server := serveBuildFixture(t, map[string]string{"/download/": listing})
	manifest := loadBuildManifestWithVars(t, Nginx, map[string]string{"source-index": server.URL + "/download/"})

	plan, err := resolveBuildSources(server.Client(), manifest, "1.30", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveBuildSources() error = %v", err)
	}
	source := plan.Sources["nginx"]
	if plan.Version != "1.30.6" || !source.Unverified || source.Checksum != "" || source.URL != server.URL+"/download/nginx-1.30.6.tar.gz" {
		t.Fatalf("plan = %+v, source = %+v", plan, source)
	}
}

// TestApacheBuildSources verifies httpd and both APR libraries resolve with their
// checksum files, and that a pinned release absent from the current index is found
// in the archive.
func TestApacheBuildSources(t *testing.T) {
	aprListing := `apr-1.7.6.tar.gz.sha256 apr-1.6.5.tar.gz.sha256 apr-util-1.6.5.tar.gz.sha256`
	server := serveBuildFixture(t, map[string]string{
		"/httpd/":   `httpd-2.4.69.tar.gz httpd-2.4.69.tar.gz.sha256`,
		"/archive/": `httpd-2.4.60.tar.gz.sha256 httpd-2.4.69.tar.gz.sha256`,
		"/apr/":     aprListing,
	})
	manifest := loadBuildManifestWithVars(t, Apache, map[string]string{
		"source-index":   server.URL + "/httpd/",
		"source-archive": server.URL + "/archive/",
		"apr-index":      server.URL + "/apr/",
	})

	plan, err := resolveBuildSources(server.Client(), manifest, "2.4", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveBuildSources() error = %v", err)
	}
	if plan.Version != "2.4.69" {
		t.Fatalf("version = %q, want 2.4.69", plan.Version)
	}
	for name, wantURL := range map[string]string{
		"httpd":    server.URL + "/httpd/httpd-2.4.69.tar.gz",
		"apr":      server.URL + "/apr/apr-1.7.6.tar.gz",
		"apr-util": server.URL + "/apr/apr-util-1.6.5.tar.gz",
	} {
		source := plan.Sources[name]
		if source.URL != wantURL || source.ChecksumURL != wantURL+".sha256" || source.ChecksumAlgorithm != checksumAlgorithmSHA256 {
			t.Fatalf("%s source = %+v, want URL %s", name, source, wantURL)
		}
	}

	pinned, err := resolveBuildSources(server.Client(), manifest, "2.4.60", "linux", "amd64")
	if err != nil {
		t.Fatalf("resolveBuildSources(pinned) error = %v", err)
	}
	if want := server.URL + "/archive/httpd-2.4.60.tar.gz"; pinned.Sources["httpd"].URL != want {
		t.Fatalf("pinned URL = %q, want %q", pinned.Sources["httpd"].URL, want)
	}
}

// TestBuildSourcesHookResultValidation covers plans a script may return wrongly.
// A checksum must never be dropped by a typo or by leaving it out.
func TestBuildSourcesHookResultValidation(t *testing.T) {
	good := `"url": "https://example.test/demo.tar.gz", "filename": "demo.tar.gz", "checksum_algorithm": "sha256", "checksum": "` + phpTestChecksum("demo") + `"`
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "valid", body: `{"version": "1.0", "sources": {"demo": {` + good + `}}}`},
		{name: "not a dict", body: `"nope"`, want: "must be a dict"},
		{name: "no version", body: `{"sources": {"demo": {` + good + `}}}`, want: "requires version"},
		{name: "no sources", body: `{"version": "1.0"}`, want: "requires sources"},
		{name: "unknown key", body: `{"version": "1.0", "extra": 1, "sources": {"demo": {` + good + `}}}`, want: "unsupported key"},
		{name: "missing stage source", body: `{"version": "1.0", "sources": {"other": {` + good + `}}}`, want: `no source "demo"`},
		{name: "unsafe version", body: `{"version": "../1", "sources": {"demo": {` + good + `}}}`, want: "invalid demo version"},
		{name: "no checksum", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.tar.gz", "filename": "d.tar.gz"}}}`, want: "explicit unverified=True"},
		{name: "checksum typo", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.tar.gz", "filename": "d.tar.gz", "chksum": "x"}}}`, want: "unsupported key"},
		{name: "bad checksum", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.tar.gz", "filename": "d.tar.gz", "checksum_algorithm": "sha256", "checksum": "zz"}}}`, want: "checksum"},
		{name: "bad algorithm", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.tar.gz", "filename": "d.tar.gz", "checksum_algorithm": "crc32", "checksum": "zz"}}}`, want: "unsupported checksum_algorithm"},
		{name: "unverified with checksum", body: `{"version": "1.0", "sources": {"demo": {` + good + `, "unverified": True}}}`, want: "cannot be unverified"},
		{name: "path in filename", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.tar.gz", "filename": "../d.tar.gz", "unverified": True}}}`, want: "invalid filename"},
		{name: "bad url", body: `{"version": "1.0", "sources": {"demo": {"url": "file:///etc/passwd", "filename": "d.tar.gz", "unverified": True}}}`, want: "absolute http or https"},
		{name: "zip source", body: `{"version": "1.0", "sources": {"demo": {"url": "https://example.test/d.zip", "filename": "d.zip", "unverified": True}}}`, want: "use tar.gz or tar.xz"},
	}

	manifestText := buildManifestFixture("build:\n  platforms: [linux-amd64]\n  stages:\n    - name: demo\n      source: demo\n")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := loadScriptFixture(t, manifestText, "def build_sources(ctx):\n    return "+test.body+"\n")
			_, err := resolveBuildSources(nil, manifest, "1", "linux", "amd64")
			if test.want == "" {
				if err != nil {
					t.Fatalf("resolveBuildSources() error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resolveBuildSources() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

// TestBuildSourcesHookMustBeDeclared verifies the script contract covers the hook:
// defining it without declaring it is an error, like every other hook.
func TestBuildSourcesHookMustBeDeclared(t *testing.T) {
	manifestText := "id: demo\ninstall-candidates:\n  all:\n    - demo\nscript:\n  file: scripts/demo.star\n  hooks: [validate]\n"
	source := "def validate(env):\n    pass\ndef build_sources(ctx):\n    return {}\n"
	manifest := loadScriptFixture(t, manifestText, source)
	if _, err := manifest.scriptModule(); err == nil || !strings.Contains(err.Error(), "not declared") {
		t.Fatalf("scriptModule() error = %v, want undeclared hook", err)
	}
}
