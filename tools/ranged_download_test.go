package tools

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testChunkSize = 1024
	testETag      = `"polka-test-etag"`
)

// withTemporaryValue overrides a package variable for one test.
func withTemporaryValue[T any](t *testing.T, target *T, value T) {
	t.Helper()
	original := *target
	*target = value
	t.Cleanup(func() { *target = original })
}

// useTestRangedSettings makes transfers fast and small, with a private budget so
// that tests cannot affect each other through the package-level pool.
func useTestRangedSettings(t *testing.T) {
	t.Helper()
	t.Setenv(downloadConnectionsEnv, "")
	withTemporaryValue(t, &rangedDownloadChunkSize, int64(testChunkSize))
	withTemporaryValue(t, &rangedDownloadSlots, make(chan struct{}, 8))
	withTemporaryValue(t, &downloadProgressInterval, time.Millisecond)
	withTemporaryDuration(t, &downloadRetryBaseDelay, 0)
}

// testPayload returns deterministic bytes in which any misplaced chunk is detectable.
func testPayload(size int) []byte {
	payload := make([]byte, size)
	for i := range payload {
		payload[i] = byte((i*31 + i/testChunkSize) % 251)
	}
	return payload
}

// serveTestFile answers with Go's own Range implementation and a strong ETag.
func serveTestFile(w http.ResponseWriter, r *http.Request, etag string, payload []byte) {
	w.Header().Set("ETag", etag)
	http.ServeContent(w, r, "payload.bin", time.Time{}, bytes.NewReader(payload))
}

// requestLog records the Range header of every request, in arrival order.
type requestLog struct {
	mu     sync.Mutex
	ranges []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ranges = append(l.ranges, r.Header.Get("Range"))
}

func (l *requestLog) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.ranges)
}

// countOf returns how many requests carried exactly this Range header value, where
// an empty value means plain GETs.
func (l *requestLog) countOf(rangeHeader string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, value := range l.ranges {
		if value == rangeHeader {
			count++
		}
	}
	return count
}

// downloadAndRead runs a download against server and returns the file's contents.
func downloadAndRead(t *testing.T, server *httptest.Server) []byte {
	t.Helper()
	target := filepath.Join(t.TempDir(), "nested", "payload.bin")
	if err := downloadFile(server.Client(), server.URL+"/payload.bin", target); err != nil {
		t.Fatalf("downloadFile() error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return data
}

// assertPayload fails with a short message instead of dumping two large byte slices.
func assertPayload(t *testing.T, got, want []byte) {
	t.Helper()
	if !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes differ from expected %d bytes", len(got), len(want))
	}
}

// TestDownloadFileRangedAssemblesChunksConcurrently verifies correct assembly, that
// transfers really overlap, and that later chunks carry If-Range.
func TestDownloadFileRangedAssemblesChunksConcurrently(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(10*testChunkSize + 37)

	var inFlight, peak atomic.Int64
	var missingIfRange atomic.Int64
	log := &requestLog{}
	// A worker waits for a sibling to join it, which proves overlap deterministically
	// instead of depending on timing. The timeout keeps a regression from hanging.
	overlap := make(chan struct{})
	var overlapOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := peak.Load()
			if current <= seen || peak.CompareAndSwap(seen, current) {
				break
			}
		}
		if r.Header.Get("Range") != fmt.Sprintf("bytes=0-%d", testChunkSize-1) {
			if r.Header.Get("If-Range") != testETag {
				missingIfRange.Add(1)
			}
			if current >= 2 {
				overlapOnce.Do(func() { close(overlap) })
			}
			select {
			case <-overlap:
			case <-time.After(2 * time.Second):
			}
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)

	if got := log.count(); got != 11 {
		t.Fatalf("requests = %d, want one per chunk (11)", got)
	}
	if got := log.countOf(""); got != 0 {
		t.Fatalf("plain GETs = %d, want 0", got)
	}
	if peak.Load() < 2 {
		t.Fatalf("peak concurrent requests = %d, want at least 2", peak.Load())
	}
	if got := missingIfRange.Load(); got != 0 {
		t.Fatalf("%d chunk requests lacked If-Range", got)
	}
}

// TestDownloadFileRangeIgnoredUsesProbeBody verifies that a server without Range
// support costs exactly one request, because the probe's body is the whole file.
func TestDownloadFileRangeIgnoredUsesProbeBody(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(5 * testChunkSize)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// TestDownloadFileFileWithinOneChunk verifies a small file finishes with its probe.
func TestDownloadFileFileWithinOneChunk(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(testChunkSize / 2)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.count(); got != 1 {
		t.Fatalf("requests = %d, want 1", got)
	}
}

// TestDownloadFileUnusableProbeFallsBackToPlainGet covers every probe answer that
// must be discarded in favor of a plain GET, since it may be a rejection of Range.
func TestDownloadFileUnusableProbeFallsBackToPlainGet(t *testing.T) {
	payload := testPayload(5 * testChunkSize)
	total := len(payload)

	// Each case answers only the Range request; the plain GET always succeeds.
	cases := map[string]func(w http.ResponseWriter){
		"no validator": func(w http.ResponseWriter) {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", testChunkSize-1, total))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:testChunkSize])
		},
		"short range leaves a hole": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-99/%d", total))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:100])
		},
		"unknown total": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/*", testChunkSize-1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:testChunkSize])
		},
		"malformed content range": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", "nonsense")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:testChunkSize])
		},
		"encoded partial content": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Encoding", "gzip")
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", testChunkSize-1, total))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:testChunkSize])
		},
		"encoded full response": func(w http.ResponseWriter) {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte("raw gzip bytes that must never reach disk"))
		},
		"range not satisfiable": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		},
		"waf rejects range": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
		},
	}

	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			useTestRangedSettings(t)
			log := &requestLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				if r.Header.Get("Range") != "" {
					answer(w)
					return
				}
				_, _ = w.Write(payload)
			}))
			defer server.Close()

			assertPayload(t, downloadAndRead(t, server), payload)
			if got := log.countOf(""); got != 1 {
				t.Fatalf("plain GETs = %d, want 1", got)
			}
			if got := log.count(); got != 2 {
				t.Fatalf("requests = %d, want the probe plus one plain GET", got)
			}
		})
	}
}

// TestDownloadFileDefiniteStatusesDoNotFallBack verifies that a missing file fails on
// the probe, since asking again without Range could not change the answer.
func TestDownloadFileDefiniteStatusesDoNotFallBack(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusGone} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			useTestRangedSettings(t)
			log := &requestLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				w.WriteHeader(status)
			}))
			defer server.Close()

			target := filepath.Join(t.TempDir(), "payload.bin")
			err := downloadFile(server.Client(), server.URL+"/payload.bin", target)
			if err == nil || !strings.Contains(err.Error(), "unexpected status") {
				t.Fatalf("downloadFile() error = %v, want unexpected status", err)
			}
			if got := log.count(); got != 1 {
				t.Fatalf("requests = %d, want 1", got)
			}
		})
	}
}

// TestDownloadFileRetriesTruncatedChunk verifies that a connection dropped mid-body
// repeats only that chunk, without restarting the file.
func TestDownloadFileRetriesTruncatedChunk(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(4 * testChunkSize)
	targetRange := fmt.Sprintf("bytes=%d-%d", 2*testChunkSize, 3*testChunkSize-1)
	var dropped atomic.Bool
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if r.Header.Get("Range") == targetRange && dropped.CompareAndSwap(false, true) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", 2*testChunkSize, 3*testChunkSize-1, len(payload)))
			w.Header().Set("Content-Length", fmt.Sprint(testChunkSize))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[2*testChunkSize : 2*testChunkSize+10])
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.countOf(targetRange); got != 2 {
		t.Fatalf("attempts at dropped chunk = %d, want 2", got)
	}
	if got := log.countOf(""); got != 0 {
		t.Fatalf("plain GETs = %d, want 0", got)
	}
}

// TestDownloadFileChangedFileRestarts verifies that a new upstream version mid-transfer
// produces only the new file. The new file is shorter, which also proves the restart
// truncates the space that the ranged attempt allocated.
func TestDownloadFileChangedFileRestarts(t *testing.T) {
	useTestRangedSettings(t)
	original := testPayload(6 * testChunkSize)
	replacement := bytes.Repeat([]byte("new"), testChunkSize)
	log := &requestLog{}
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if requests.Add(1) == 1 {
			serveTestFile(w, r, testETag, original)
			return
		}
		// With the old ETag in If-Range, ServeContent answers 200 with the full file.
		serveTestFile(w, r, `"polka-test-etag-2"`, replacement)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), replacement)
	if got := log.countOf(""); got != 1 {
		t.Fatalf("plain GETs = %d, want 1", got)
	}
}

// TestDownloadFileChunkAnswersThatFallBack covers chunk responses that retrying can't
// fix, or where more pressure on the server would not help.
func TestDownloadFileChunkAnswersThatFallBack(t *testing.T) {
	payload := testPayload(4 * testChunkSize)
	cases := map[string]func(w http.ResponseWriter){
		"wrong content range": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", testChunkSize-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:testChunkSize])
		},
		"wrong total": func(w http.ResponseWriter) {
			w.Header().Set("ETag", testETag)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", testChunkSize, 2*testChunkSize-1, len(payload)+1))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[testChunkSize : 2*testChunkSize])
		},
		"different validator on 206": func(w http.ResponseWriter) {
			w.Header().Set("ETag", `"other"`)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", testChunkSize, 2*testChunkSize-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[testChunkSize : 2*testChunkSize])
		},
		"rate limited": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusTooManyRequests)
		},
		"unavailable": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusServiceUnavailable)
		},
		"forbidden": func(w http.ResponseWriter) {
			w.WriteHeader(http.StatusForbidden)
		},
	}

	targetRange := fmt.Sprintf("bytes=%d-%d", testChunkSize, 2*testChunkSize-1)
	for name, answer := range cases {
		t.Run(name, func(t *testing.T) {
			useTestRangedSettings(t)
			log := &requestLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				if r.Header.Get("Range") == targetRange {
					answer(w)
					return
				}
				serveTestFile(w, r, testETag, payload)
			}))
			defer server.Close()

			assertPayload(t, downloadAndRead(t, server), payload)
			if got := log.countOf(targetRange); got != 1 {
				t.Fatalf("attempts at bad chunk = %d, want 1 with no retry", got)
			}
			if got := log.countOf(""); got != 1 {
				t.Fatalf("plain GETs = %d, want 1", got)
			}
		})
	}
}

// TestDownloadFilePersistentChunkFailureFallsBack verifies that exhausting retries on a
// chunk still ends in a successful download.
func TestDownloadFilePersistentChunkFailureFallsBack(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(4 * testChunkSize)
	targetRange := fmt.Sprintf("bytes=%d-%d", testChunkSize, 2*testChunkSize-1)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if r.Header.Get("Range") == targetRange {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.countOf(targetRange); got != downloadMaxAttempts {
		t.Fatalf("attempts at failing chunk = %d, want %d", got, downloadMaxAttempts)
	}
	if got := log.countOf(""); got != 1 {
		t.Fatalf("plain GETs = %d, want 1", got)
	}
}

// TestDownloadFileStalledChunkIsRetried verifies that a silent connection cannot hang a
// transfer, both on the probe's body, which has its own context, and on a later chunk.
func TestDownloadFileStalledChunkIsRetried(t *testing.T) {
	cases := map[string]string{
		"probe body":  fmt.Sprintf("bytes=0-%d", testChunkSize-1),
		"later chunk": fmt.Sprintf("bytes=%d-%d", testChunkSize, 2*testChunkSize-1),
	}
	for name, targetRange := range cases {
		t.Run(name, func(t *testing.T) {
			useTestRangedSettings(t)
			withTemporaryDuration(t, &downloadStallTimeout, 50*time.Millisecond)
			payload := testPayload(3 * testChunkSize)
			var stalled atomic.Bool
			log := &requestLog{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				log.add(r)
				if r.Header.Get("Range") == targetRange && stalled.CompareAndSwap(false, true) {
					// Send valid headers and a little data, then go silent until the
					// client gives up on this connection.
					var start, end int
					if _, err := fmt.Sscanf(targetRange, "bytes=%d-%d", &start, &end); err != nil {
						t.Errorf("parse range: %v", err)
						return
					}
					w.Header().Set("ETag", testETag)
					w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
					w.WriteHeader(http.StatusPartialContent)
					_, _ = w.Write(payload[start : start+10])
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				serveTestFile(w, r, testETag, payload)
			}))
			defer server.Close()

			assertPayload(t, downloadAndRead(t, server), payload)
			if got := log.countOf(targetRange); got != 2 {
				t.Fatalf("attempts at stalled chunk = %d, want 2", got)
			}
			if got := log.countOf(""); got != 0 {
				t.Fatalf("plain GETs = %d, want 0", got)
			}
		})
	}
}

// singleStreamRoutes lists the ways a download ends up as a single stream: through a
// connection override, or through the server's answer to a Range request.
var singleStreamRoutes = map[string]struct {
	connections string
	rejectRange bool
}{
	"server ignores range": {},
	"off switch":           {connections: "1"},
	"fallback from probe":  {rejectRange: true},
}

// TestDownloadFileSingleStreamOutlivesClientTimeout verifies that a slow transfer that
// keeps moving succeeds even though it takes longer than both the client's total Timeout
// and the stall timeout. A total limit used to fail large downloads on slow links.
func TestDownloadFileSingleStreamOutlivesClientTimeout(t *testing.T) {
	for name, route := range singleStreamRoutes {
		t.Run(name, func(t *testing.T) {
			useTestRangedSettings(t)
			t.Setenv(downloadConnectionsEnv, route.connections)
			withTemporaryDuration(t, &downloadStallTimeout, 500*time.Millisecond)
			const pieces = 30
			payload := testPayload(pieces * 10)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if route.rejectRange && r.Header.Get("Range") != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				for i := range pieces {
					_, _ = w.Write(payload[i*10 : (i+1)*10])
					w.(http.Flusher).Flush()
					time.Sleep(20 * time.Millisecond)
				}
			}))
			defer server.Close()

			client := server.Client()
			client.Timeout = 100 * time.Millisecond
			target := filepath.Join(t.TempDir(), "payload.bin")
			if err := downloadFile(client, server.URL, target); err != nil {
				t.Fatalf("downloadFile() error = %v", err)
			}
			data, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			assertPayload(t, data, payload)
		})
	}
}

// TestDownloadFileSingleStreamFailsOnStall verifies that removing the total limit cannot
// let a silent connection hang forever, and that the error names the actual problem.
func TestDownloadFileSingleStreamFailsOnStall(t *testing.T) {
	for name, route := range singleStreamRoutes {
		t.Run(name, func(t *testing.T) {
			useTestRangedSettings(t)
			t.Setenv(downloadConnectionsEnv, route.connections)
			withTemporaryDuration(t, &downloadStallTimeout, 50*time.Millisecond)
			payload := testPayload(2 * testChunkSize)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if route.rejectRange && r.Header.Get("Range") != "" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				_, _ = w.Write(payload[:testChunkSize])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer server.Close()

			target := filepath.Join(t.TempDir(), "payload.bin")
			err := downloadFile(server.Client(), server.URL, target)
			if err == nil || !strings.Contains(err.Error(), "no data received") {
				t.Fatalf("downloadFile() error = %v, want a stall error", err)
			}
		})
	}
}

// TestDownloadFileHeaderStallIsRetried verifies that a server accepting the connection
// but never answering costs one attempt, because each attempt gets its own timer.
func TestDownloadFileHeaderStallIsRetried(t *testing.T) {
	useTestRangedSettings(t)
	withTemporaryDuration(t, &downloadStallTimeout, 50*time.Millisecond)
	payload := testPayload(2 * testChunkSize)
	var stalled atomic.Bool
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		if stalled.CompareAndSwap(false, true) {
			<-r.Context().Done()
			return
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	// The stalled attempt, the retried probe, and the second chunk.
	if got := log.count(); got != 3 {
		t.Fatalf("requests = %d, want 3", got)
	}
}

// TestDownloadFileHeaderStallExhaustsRetries verifies that a host that never answers
// fails after its attempts, with an error naming the problem.
func TestDownloadFileHeaderStallExhaustsRetries(t *testing.T) {
	useTestRangedSettings(t)
	withTemporaryDuration(t, &downloadStallTimeout, 20*time.Millisecond)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		<-r.Context().Done()
	}))
	defer server.Close()

	target := filepath.Join(t.TempDir(), "payload.bin")
	err := downloadFile(server.Client(), server.URL, target)
	if err == nil || !strings.Contains(err.Error(), "no data received") {
		t.Fatalf("downloadFile() error = %v, want a stall error", err)
	}
	if got := log.count(); got != downloadMaxAttempts {
		t.Fatalf("requests = %d, want %d", got, downloadMaxAttempts)
	}
}

// TestWithStallGuardKeepsOriginalClient verifies that only file transfers lose the total
// limit. The original client stays as it was, so metadata requests keep theirs.
func TestWithStallGuardKeepsOriginalClient(t *testing.T) {
	client := newDownloadClient()
	transport := client.Transport

	guarded := withStallGuard(client)

	if guarded.Timeout != 0 {
		t.Fatalf("guarded Timeout = %s, want none", guarded.Timeout)
	}
	if client.Timeout != downloadClientTimeout {
		t.Fatalf("original Timeout = %s, want %s", client.Timeout, downloadClientTimeout)
	}
	if client.Transport != transport {
		t.Fatal("original Transport was replaced")
	}
	if got := withStallGuard(&http.Client{}).Transport.(*stallGuardTransport).base; got != http.DefaultTransport {
		t.Fatalf("base transport for a client without one = %v, want the default", got)
	}
}

// TestDownloadFileTargetErrorDoesNotFallBack verifies that a local failure skips the
// plain GET, since downloading the file again could not fix it.
func TestDownloadFileTargetErrorDoesNotFallBack(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(3 * testChunkSize)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	// A directory at the target path makes opening the file fail.
	target := t.TempDir()
	if err := downloadFile(server.Client(), server.URL, target); err == nil {
		t.Fatal("downloadFile() error = nil, want a local error")
	}
	if got := log.count(); got != 1 {
		t.Fatalf("requests = %d, want only the probe", got)
	}
}

// TestTransferChunkDoesNotRetryWriteErrors verifies that a full-disk style failure stops
// at once rather than spending retries. A closed file makes every write fail.
func TestTransferChunkDoesNotRetryWriteErrors(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(3 * testChunkSize)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	file, err := os.Create(filepath.Join(t.TempDir(), "payload.bin"))
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	ctx, cancel := t.Context(), func() {}
	transfer := &rangedTransfer{
		client:     server.Client(),
		url:        server.URL,
		targetPath: file.Name(),
		total:      int64(len(payload)),
		chunkSize:  testChunkSize,
		validator:  rangeValidator{header: "ETag", value: testETag},
		reporter:   startDownloadReporter(nil),
		file:       file,
		ctx:        ctx,
		cancel:     cancel,
	}

	err = transfer.transferChunk(1, &transferSlot{})
	var writeErr *downloadWriteError
	if !errors.As(err, &writeErr) {
		t.Fatalf("transferChunk() error = %v, want downloadWriteError", err)
	}
	if got := log.count(); got != 1 {
		t.Fatalf("requests = %d, want 1 with no retry", got)
	}
	if got := transfer.reporter.downloaded.Load(); got != 0 {
		t.Fatalf("reported bytes after failure = %d, want 0", got)
	}
}

// TestDownloadFilesShareConnectionBudget verifies that simultaneous downloads stay
// within one connection per file plus the shared budget, and return every slot.
func TestDownloadFilesShareConnectionBudget(t *testing.T) {
	useTestRangedSettings(t)
	const budget = 2
	withTemporaryValue(t, &rangedDownloadSlots, make(chan struct{}, budget))
	payload := testPayload(12 * testChunkSize)

	var inFlight, peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			seen := peak.Load()
			if current <= seen || peak.CompareAndSwap(seen, current) {
				break
			}
		}
		// Hold each request long enough for workers to pile up against the budget.
		time.Sleep(5 * time.Millisecond)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	const files = 3
	var group sync.WaitGroup
	for range files {
		group.Go(func() {
			assertPayload(t, downloadAndRead(t, server), payload)
		})
	}
	group.Wait()

	if got := peak.Load(); got > files+budget {
		t.Fatalf("peak connections = %d, want at most %d", got, files+budget)
	}
	if got := len(rangedDownloadSlots); got != 0 {
		t.Fatalf("slots still held after downloads = %d, want 0", got)
	}
}

// TestDownloadFileFirstWorkerNeedsNoSlot verifies that an exhausted budget slows a file
// down without blocking it, and that workers waiting for a slot still exit promptly.
func TestDownloadFileFirstWorkerNeedsNoSlot(t *testing.T) {
	useTestRangedSettings(t)
	slots := make(chan struct{}, 2)
	slots <- struct{}{}
	slots <- struct{}{}
	withTemporaryValue(t, &rangedDownloadSlots, slots)
	payload := testPayload(5 * testChunkSize)

	var inFlight, peak atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := inFlight.Add(1)
		defer inFlight.Add(-1)
		if current > peak.Load() {
			peak.Store(current)
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak connections = %d, want 1 with no budget left", got)
	}
}

// TestDownloadFileChunksRestartFromOriginalURL verifies that every chunk goes through
// the original URL and that Range and If-Range survive the redirect, which is what
// lets an expired signed CDN URL be issued again.
func TestDownloadFileChunksRestartFromOriginalURL(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(4 * testChunkSize)
	var originRequests, signedRequests atomic.Int64
	var missingHeaders atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/payload.bin", func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		http.Redirect(w, r, "/signed", http.StatusFound)
	})
	mux.HandleFunc("/signed", func(w http.ResponseWriter, r *http.Request) {
		signedRequests.Add(1)
		rangeHeader := r.Header.Get("Range")
		isProbe := strings.HasPrefix(rangeHeader, "bytes=0-")
		if rangeHeader == "" || (!isProbe && r.Header.Get("If-Range") != testETag) {
			missingHeaders.Add(1)
		}
		serveTestFile(w, r, testETag, payload)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if origin, signed := originRequests.Load(), signedRequests.Load(); origin != 4 || signed != 4 {
		t.Fatalf("origin requests = %d, signed requests = %d, want 4 of each", origin, signed)
	}
	if got := missingHeaders.Load(); got != 0 {
		t.Fatalf("%d requests lost their range headers on redirect", got)
	}
}

// TestDownloadFileUsesLastModifiedWithoutETag verifies the second validator, including
// that it is the value sent in If-Range.
func TestDownloadFileUsesLastModifiedWithoutETag(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(3 * testChunkSize)
	modified := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		http.ServeContent(w, r, "payload.bin", modified, bytes.NewReader(payload))
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.count(); got != 3 {
		t.Fatalf("requests = %d, want one per chunk (3)", got)
	}
	if got := log.countOf(""); got != 0 {
		t.Fatalf("plain GETs = %d, want 0", got)
	}
}

// TestDownloadFileWeakETagIsNotAValidator verifies that a weak ETag alone falls back,
// as weak validators cannot guarantee byte-identical ranges.
func TestDownloadFileWeakETagIsNotAValidator(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(3 * testChunkSize)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		serveTestFile(w, r, `W/"weak"`, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got := log.countOf(""); got != 1 {
		t.Fatalf("plain GETs = %d, want 1", got)
	}
}

// TestDownloadConnectionsEnv verifies the override, including its off switch.
func TestDownloadConnectionsEnv(t *testing.T) {
	for value, want := range map[string]int{
		"":     defaultDownloadConnections,
		"1":    1,
		"2":    2,
		" 3 ":  3,
		"0":    defaultDownloadConnections,
		"-1":   defaultDownloadConnections,
		"many": defaultDownloadConnections,
	} {
		t.Setenv(downloadConnectionsEnv, value)
		if got := downloadConnections(); got != want {
			t.Errorf("downloadConnections(%q) = %d, want %d", value, got, want)
		}
	}
}

// TestDownloadFileOffSwitchSendsNoRange verifies that one connection means no Range.
func TestDownloadFileOffSwitchSendsNoRange(t *testing.T) {
	useTestRangedSettings(t)
	t.Setenv(downloadConnectionsEnv, "1")
	payload := testPayload(3 * testChunkSize)
	log := &requestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.add(r)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	assertPayload(t, downloadAndRead(t, server), payload)
	if got, plain := log.count(), log.countOf(""); got != 1 || plain != 1 {
		t.Fatalf("requests = %d, plain GETs = %d, want a single plain GET", got, plain)
	}
}

// TestDownloadTransportUsesHTTP1 verifies that the transport speaks HTTP/1.1 to a server
// preferring HTTP/2. A plain SetHTTP1 on a clone still offers h2 in ALPN and fails such
// servers, which is most download hosts, so a successful response matters here too. The
// control request proves this server really would select h2.
func TestDownloadTransportUsesHTTP1(t *testing.T) {
	var lastProto atomic.Int64
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastProto.Store(int64(r.ProtoMajor))
		_, _ = w.Write([]byte("ok"))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	control, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatalf("control request error = %v", err)
	}
	if err := control.Body.Close(); err != nil {
		t.Fatalf("control close error = %v", err)
	}
	if got := lastProto.Load(); got != 2 {
		t.Fatalf("control request used HTTP/%d, want HTTP/2 so this test can detect a regression", got)
	}

	transport := newDownloadTransport()
	defer transport.CloseIdleConnections()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport.TLSClientConfig.RootCAs = roots

	response, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Get(server.URL)
	if err != nil {
		t.Fatalf("request through download transport error = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response error = %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want ok", body)
	}
	if got := lastProto.Load(); got != 1 {
		t.Fatalf("request used HTTP/%d, want HTTP/1", got)
	}
}

// progressRecorder collects events, with a lock because they arrive on another goroutine.
type progressRecorder struct {
	mu     sync.Mutex
	events []DownloadProgress
	closed atomic.Bool
	late   atomic.Int64
}

func (r *progressRecorder) record(event DownloadProgress) {
	if r.closed.Load() {
		r.late.Add(1)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *progressRecorder) snapshot() []DownloadProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]DownloadProgress(nil), r.events...)
}

// TestDownloadFileProgressRanged verifies ordering and the final totals on a failure-free
// run, and that no event arrives once the download has returned.
func TestDownloadFileProgressRanged(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(20 * testChunkSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Spread the transfer across several reporter ticks.
		time.Sleep(2 * time.Millisecond)
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	recorder := &progressRecorder{}
	target := filepath.Join(t.TempDir(), "payload.bin")
	if err := downloadFileWithProgress(server.Client(), server.URL, target, recorder.record); err != nil {
		t.Fatalf("downloadFileWithProgress() error = %v", err)
	}
	recorder.closed.Store(true)
	// Allow any incorrectly detached delivery goroutine time to misbehave.
	time.Sleep(5 * downloadProgressInterval)

	if got := recorder.late.Load(); got != 0 {
		t.Fatalf("%d events arrived after the download returned", got)
	}
	events := recorder.snapshot()
	if len(events) == 0 {
		t.Fatal("no progress events")
	}
	for i := 1; i < len(events); i++ {
		if events[i].Downloaded < events[i-1].Downloaded {
			t.Fatalf("progress went backwards: %+v then %+v", events[i-1], events[i])
		}
	}
	final := events[len(events)-1]
	want := DownloadProgress{Downloaded: int64(len(payload)), Total: int64(len(payload))}
	if final != want {
		t.Fatalf("final progress = %+v, want %+v", final, want)
	}
}

// TestDownloadFileProgressResetsOnFallback verifies that bytes from the abandoned ranged
// attempt are not counted on top of the restarted download.
func TestDownloadFileProgressResetsOnFallback(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(4 * testChunkSize)
	targetRange := fmt.Sprintf("bytes=%d-%d", 3*testChunkSize, 4*testChunkSize-1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == targetRange {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		serveTestFile(w, r, testETag, payload)
	}))
	defer server.Close()

	recorder := &progressRecorder{}
	target := filepath.Join(t.TempDir(), "payload.bin")
	if err := downloadFileWithProgress(server.Client(), server.URL, target, recorder.record); err != nil {
		t.Fatalf("downloadFileWithProgress() error = %v", err)
	}

	events := recorder.snapshot()
	final := events[len(events)-1]
	want := DownloadProgress{Downloaded: int64(len(payload)), Total: int64(len(payload))}
	if final != want {
		t.Fatalf("final progress = %+v, want %+v", final, want)
	}
}

// TestDownloadFileProgressUnknownTotal verifies that a stream of undeclared length reports
// a total of 0, as the spinner shows bytes without a percentage for that.
func TestDownloadFileProgressUnknownTotal(t *testing.T) {
	useTestRangedSettings(t)
	payload := testPayload(3 * testChunkSize)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Flushing before the handler ends forces chunked encoding.
		_, _ = w.Write(payload[:testChunkSize])
		w.(http.Flusher).Flush()
		_, _ = w.Write(payload[testChunkSize:])
	}))
	defer server.Close()

	recorder := &progressRecorder{}
	target := filepath.Join(t.TempDir(), "payload.bin")
	if err := downloadFileWithProgress(server.Client(), server.URL, target, recorder.record); err != nil {
		t.Fatalf("downloadFileWithProgress() error = %v", err)
	}

	events := recorder.snapshot()
	final := events[len(events)-1]
	want := DownloadProgress{Downloaded: int64(len(payload)), Total: 0}
	if final != want {
		t.Fatalf("final progress = %+v, want %+v", final, want)
	}
}

// TestParseContentRange covers the accepted form and the rejections validation relies on.
func TestParseContentRange(t *testing.T) {
	cases := map[string]struct {
		start, end, total int64
		ok                bool
	}{
		"bytes 0-1023/4096":    {0, 1023, 4096, true},
		"bytes 1024-2047/2048": {1024, 2047, 2048, true},
		"bytes 0-1023/*":       {ok: false},
		"bytes */4096":         {ok: false},
		"bytes 10-5/4096":      {ok: false},
		"bytes 0-4096/4096":    {ok: false},
		"items 0-1/2":          {ok: false},
		"":                     {ok: false},
		"nonsense":             {ok: false},
	}
	for value, want := range cases {
		start, end, total, ok := parseContentRange(value)
		if ok != want.ok || start != want.start || end != want.end || total != want.total {
			t.Errorf("parseContentRange(%q) = %d, %d, %d, %v, want %+v", value, start, end, total, ok, want)
		}
	}
}
