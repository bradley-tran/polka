package tools

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// downloadConnectionsEnv overrides how many connections one file may use. A
	// value of 1 disables Range requests, as an off switch for a host or proxy
	// that misbehaves with them.
	downloadConnectionsEnv     = "POLKA_DOWNLOAD_CONNECTIONS"
	defaultDownloadConnections = 4

	// downloadIdleConnsPerHost keeps connections reusable across chunks, because
	// the transport's default of 2 would redial for every chunk of every worker.
	downloadIdleConnsPerHost = 16
	downloadClientTimeout    = 10 * time.Minute
)

var (
	// rangedDownloadChunkSize is both the probe's size and the unit workers claim,
	// so a retry only repeats one chunk rather than the whole file.
	rangedDownloadChunkSize int64 = 8 << 20
	// downloadStallTimeout limits how long a file transfer may go without receiving
	// data. It resets on every read, so a slow but moving transfer never trips it.
	downloadStallTimeout = 2 * time.Minute
	// rangedDownloadMaxRetryDelay caps backoff between chunk attempts, because a
	// server's Retry-After is otherwise unbounded and falling back is often faster.
	rangedDownloadMaxRetryDelay = 5 * time.Second
	// downloadProgressInterval matches the install spinner's render interval.
	downloadProgressInterval = 200 * time.Millisecond
	// rangedDownloadSlots is the process-wide budget for connections beyond each
	// file's first, which stops N tools × 4 connections from hitting one host.
	rangedDownloadSlots = make(chan struct{}, 8)

	// downloadTransport is shared so that every default client reuses connections.
	downloadTransport = sync.OnceValue(newDownloadTransport)

	// errRangedUnusable marks a chunk failure that retrying cannot fix, such as the
	// file changing mid-transfer, so the download restarts as a single stream.
	errRangedUnusable = errors.New("range transfer unusable")
)

// DownloadProgress reports how much of one file has been transferred.
type DownloadProgress struct {
	Downloaded int64
	// Total is the file size in bytes, or 0 when the server did not declare it.
	Total int64
}

// newDownloadClient returns the default client for tool downloads.
func newDownloadClient() *http.Client {
	return &http.Client{Timeout: downloadClientTimeout, Transport: downloadTransport()}
}

// newDownloadTransport builds an HTTP/1.1-only transport. HTTP/2 would multiplex
// every range over one TCP connection, which defeats multi-connection downloads.
func newDownloadTransport() *http.Transport {
	var transport *http.Transport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		transport = base.Clone()
	} else {
		transport = &http.Transport{Proxy: http.ProxyFromEnvironment}
	}

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	transport.Protocols = protocols
	transport.ForceAttemptHTTP2 = false
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	}
	// The clone inherits h2 in its ALPN list. If it were still offered, a server
	// could select h2 on a transport that only speaks HTTP/1.1, and requests fail.
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.MaxIdleConnsPerHost = downloadIdleConnsPerHost

	return transport
}

// withStallGuard returns a copy of client for transferring a file. It replaces the
// client's total Timeout with a stall timeout, because a total limit fails a large
// download on a slow link even while data is still arriving. Metadata requests keep
// using the original client and its total limit.
func withStallGuard(client *http.Client) *http.Client {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	guarded := *client
	guarded.Timeout = 0
	guarded.Transport = &stallGuardTransport{base: base, timeout: downloadStallTimeout}

	return &guarded
}

// stallGuardTransport gives each round trip its own stall timer, so that every retry
// and redirect hop starts fresh and the wait for headers is covered as well.
type stallGuardTransport struct {
	base    http.RoundTripper
	timeout time.Duration
}

// RoundTrip cancels the request if no response or body data arrives within the
// timeout. The timer lives until the returned body is closed.
func (t *stallGuardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(request.Context())
	guard := &stallGuard{timeout: t.timeout, cancel: cancel}
	guard.timer = time.AfterFunc(t.timeout, guard.expire)

	response, err := t.base.RoundTrip(request.WithContext(ctx))
	if err != nil {
		guard.stop()
		return nil, guard.explain(err)
	}
	response.Body = &stallGuardBody{ReadCloser: response.Body, guard: guard}

	return response, nil
}

// stallGuard is the timer for one round trip.
type stallGuard struct {
	timeout time.Duration
	cancel  context.CancelFunc
	timer   *time.Timer
	expired atomic.Bool
}

// expire runs when the timer fires, recording the cause before cancelling.
func (g *stallGuard) expire() {
	g.expired.Store(true)
	g.cancel()
}

// stop ends the timer and releases the round trip's context.
func (g *stallGuard) stop() {
	g.timer.Stop()
	g.cancel()
}

// explain replaces the generic cancellation error a stall causes with its reason.
func (g *stallGuard) explain(err error) error {
	if g.expired.Load() {
		return fmt.Errorf("no data received for %s", g.timeout)
	}

	return err
}

// stallGuardBody pushes the stall timer back each time data arrives.
type stallGuardBody struct {
	io.ReadCloser
	guard *stallGuard
}

func (b *stallGuardBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.guard.timer.Reset(b.guard.timeout)
	}
	// EOF is returned as is, because io.Copy compares against it directly.
	if err != nil && err != io.EOF {
		return n, b.guard.explain(err)
	}

	return n, err
}

func (b *stallGuardBody) Close() error {
	b.guard.stop()
	return b.ReadCloser.Close()
}

// downloadConnections returns the per-file connection limit, treating a missing
// or invalid override as the default.
func downloadConnections() int {
	value := strings.TrimSpace(os.Getenv(downloadConnectionsEnv))
	if value == "" {
		return defaultDownloadConnections
	}
	connections, err := strconv.Atoi(value)
	if err != nil || connections < 1 {
		return defaultDownloadConnections
	}

	return connections
}

// downloadFileWithProgress downloads url to targetPath, using several connections
// when the server supports Range requests and a single stream otherwise. The
// callback is never invoked after this function returns.
func downloadFileWithProgress(client *http.Client, url, targetPath string, progress func(DownloadProgress)) error {
	client = withStallGuard(effectiveHTTPClient(client))
	reporter := startDownloadReporter(progress)
	defer reporter.stop()

	connections := downloadConnections()
	if connections <= 1 {
		return downloadSingleStream(client, url, targetPath, reporter)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chunkSize := rangedDownloadChunkSize
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	setDownloadRequestHeaders(request)
	request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", chunkSize-1))

	response, err := doDownloadRequest(client, request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}

	switch {
	case response.StatusCode == http.StatusOK && !responseIsEncoded(response):
		// The server ignored Range, so this body already is the whole file.
		defer response.Body.Close()
		return writeDownloadBody(response, targetPath, reporter)
	case response.StatusCode == http.StatusPartialContent:
		total, validator, ok := validateRangeProbe(response, chunkSize)
		if !ok {
			break
		}
		transfer := &rangedTransfer{
			client:     client,
			url:        url,
			targetPath: targetPath,
			total:      total,
			chunkSize:  chunkSize,
			validator:  validator,
			reporter:   reporter,
			ctx:        ctx,
			cancel:     cancel,
			probe:      response,
			exhausted:  make(chan struct{}),
			slots:      rangedDownloadSlots,
		}
		err := transfer.run(connections)
		var writeErr *downloadWriteError
		if err == nil || errors.As(err, &writeErr) {
			return err
		}
		reporter.reset()
		return downloadSingleStream(client, url, targetPath, reporter)
	case response.StatusCode == http.StatusNotFound, response.StatusCode == http.StatusGone, retryableDownloadStatus(response.StatusCode):
		// Dropping Range would not change these answers, so fail as a plain GET does.
		defer response.Body.Close()
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}

	// Everything else may be a rejection of Range itself, such as a WAF answering
	// 403 or a server sending an unusable 206, while a plain GET still works. The
	// body is abandoned rather than drained because it could be megabytes long, and
	// a close failure on a response we are discarding has no consequence.
	_ = response.Body.Close()
	return downloadSingleStream(client, url, targetPath, reporter)
}

// downloadSingleStream fetches url with one plain GET, without any Range header,
// which also lets Go's transport handle transparent gzip as usual.
func downloadSingleStream(client *http.Client, url, targetPath string, reporter *downloadReporter) error {
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	setDownloadRequestHeaders(request)

	response, err := doDownloadRequest(client, request)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: unexpected status %s", url, response.Status)
	}

	return writeDownloadBody(response, targetPath, reporter)
}

// writeDownloadBody streams a complete response body to targetPath, replacing any
// partial content an abandoned ranged attempt left there.
func writeDownloadBody(response *http.Response, targetPath string, reporter *downloadReporter) (err error) {
	file, err := createDownloadTarget(targetPath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close %s: %w", targetPath, closeErr)
		}
	}()

	// ContentLength is -1 when unknown, including after transparent decompression.
	if response.ContentLength > 0 {
		reporter.setTotal(response.ContentLength)
	}
	if _, err := io.Copy(file, io.TeeReader(response.Body, reporter)); err != nil {
		return fmt.Errorf("write %s: %w", targetPath, err)
	}

	return nil
}

// createDownloadTarget creates parent directories and opens an empty target file.
func createDownloadTarget(targetPath string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return nil, fmt.Errorf("create parent dir for %s: %w", targetPath, err)
	}
	file, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", targetPath, err)
	}

	return file, nil
}

// responseIsEncoded reports whether the body has a content coding applied. Range
// requests suppress Go's transparent gzip, so an encoded body would reach disk raw.
func responseIsEncoded(response *http.Response) bool {
	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	return encoding != "" && !strings.EqualFold(encoding, "identity")
}

// rangeValidator identifies one version of a remote file, so that chunks fetched
// separately cannot silently come from different versions.
type rangeValidator struct {
	// header is the response header holding the value: ETag or Last-Modified.
	header string
	value  string
}

// matches reports whether response carries the same validator value.
func (v rangeValidator) matches(response *http.Response) bool {
	return response.Header.Get(v.header) == v.value
}

// responseRangeValidator picks a strong ETag or else Last-Modified. Weak ETags
// cannot be used with If-Range, and they only promise semantic equivalence.
func responseRangeValidator(response *http.Response) (rangeValidator, bool) {
	if etag := strings.TrimSpace(response.Header.Get("ETag")); etag != "" && !strings.HasPrefix(etag, "W/") {
		return rangeValidator{header: "ETag", value: etag}, true
	}
	if modified := strings.TrimSpace(response.Header.Get("Last-Modified")); modified != "" {
		return rangeValidator{header: "Last-Modified", value: modified}, true
	}

	return rangeValidator{}, false
}

// parseContentRange parses "bytes start-end/total". A "*" total fails to parse, as
// ranged mode has to know the file size up front.
func parseContentRange(value string) (start, end, total int64, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(value), "bytes ")
	if !found {
		return 0, 0, 0, false
	}
	span, totalText, found := strings.Cut(strings.TrimSpace(spec), "/")
	if !found {
		return 0, 0, 0, false
	}
	startText, endText, found := strings.Cut(span, "-")
	if !found {
		return 0, 0, 0, false
	}
	start, err := strconv.ParseInt(startText, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	end, err = strconv.ParseInt(endText, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	total, err = strconv.ParseInt(totalText, 10, 64)
	if err != nil {
		return 0, 0, 0, false
	}
	if start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}

	return start, end, total, true
}

// validateRangeProbe checks that a 206 probe is safe to build a ranged transfer
// on. The end offset must be exact: workers start at the next chunk boundary, so a
// shorter answer would leave a hole of zeros in the file.
func validateRangeProbe(response *http.Response, chunkSize int64) (int64, rangeValidator, bool) {
	if responseIsEncoded(response) {
		return 0, rangeValidator{}, false
	}
	start, end, total, ok := parseContentRange(response.Header.Get("Content-Range"))
	if !ok || start != 0 || end != min(chunkSize, total)-1 {
		return 0, rangeValidator{}, false
	}
	validator, ok := responseRangeValidator(response)
	if !ok {
		return 0, rangeValidator{}, false
	}

	return total, validator, true
}

// downloadWriteError marks a local write failure such as a full disk, which neither
// a retry nor a fresh download could fix.
type downloadWriteError struct {
	err error
}

func (e *downloadWriteError) Error() string { return e.err.Error() }
func (e *downloadWriteError) Unwrap() error { return e.err }

// chunkStatusError is a retryable HTTP status on a chunk, with the delay to honor.
type chunkStatusError struct {
	status string
	delay  time.Duration
}

func (e *chunkStatusError) Error() string { return "unexpected status " + e.status }

// transferSlot tracks one worker's share of the connection budget. A nil slots
// channel means the worker needs no slot, which is how each file's first worker
// always makes progress regardless of how busy other downloads are.
type transferSlot struct {
	slots chan struct{}
	held  bool
}

// acquire waits for a slot. It returns false if ctx ends or stop closes first; a
// nil stop channel never closes, for callers that must get a slot eventually.
func (s *transferSlot) acquire(ctx context.Context, stop <-chan struct{}) bool {
	if s.slots == nil || s.held {
		return true
	}
	select {
	case s.slots <- struct{}{}:
		s.held = true
		return true
	case <-ctx.Done():
		return false
	case <-stop:
		return false
	}
}

// release returns a held slot, and is safe to call when none is held.
func (s *transferSlot) release() {
	if s.slots == nil || !s.held {
		return
	}
	<-s.slots
	s.held = false
}

// rangedTransfer downloads one file as fixed-size chunks over several connections.
type rangedTransfer struct {
	client     *http.Client
	url        string
	targetPath string
	total      int64
	chunkSize  int64
	validator  rangeValidator
	reporter   *downloadReporter
	file       *os.File
	slots      chan struct{}

	// ctx ends when any worker fails, stopping its siblings.
	ctx    context.Context
	cancel context.CancelFunc

	// probe holds the first response, whose body is chunk 0's first attempt.
	probe *http.Response

	chunks        int64
	nextChunk     atomic.Int64
	exhausted     chan struct{}
	exhaustedOnce sync.Once

	failOnce sync.Once
	failure  error
}

// run performs the transfer. It returns only after every worker has exited and the
// file is closed, so a fallback can safely truncate and rewrite the same path.
func (t *rangedTransfer) run(connections int) (err error) {
	// Once workers start, chunk 0's first attempt owns the probe body. Until then a
	// return here must close it, and a close failure on a body nobody has read from
	// has no consequence.
	probeConsumed := false
	defer func() {
		if !probeConsumed {
			_ = t.probe.Body.Close()
		}
	}()

	file, err := createDownloadTarget(t.targetPath)
	if err != nil {
		return &downloadWriteError{err: err}
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			err = &downloadWriteError{err: fmt.Errorf("close %s: %w", t.targetPath, closeErr)}
		}
	}()
	if err := file.Truncate(t.total); err != nil {
		return &downloadWriteError{err: fmt.Errorf("allocate %s: %w", t.targetPath, err)}
	}

	t.file = file
	t.chunks = (t.total + t.chunkSize - 1) / t.chunkSize
	t.nextChunk.Store(1) // The first worker owns chunk 0.
	t.reporter.setTotal(t.total)

	workers := min(int64(connections), t.chunks, int64(cap(t.slots))+1)
	probeConsumed = true

	var group sync.WaitGroup
	group.Go(func() {
		first := &transferSlot{}
		if err := t.transferChunk(0, first); err != nil {
			t.fail(err)
			return
		}
		t.work(first)
	})
	for range workers - 1 {
		group.Go(func() {
			t.work(&transferSlot{slots: t.slots})
		})
	}
	group.Wait()

	return t.failure
}

// work claims and transfers chunks until none remain or the transfer fails. The slot
// is taken per chunk so that concurrent downloads share the budget between them.
func (t *rangedTransfer) work(slot *transferSlot) {
	defer slot.release()
	for {
		if !slot.acquire(t.ctx, t.exhausted) || t.ctx.Err() != nil {
			return
		}
		index, ok := t.claimChunk()
		if !ok {
			return
		}
		if err := t.transferChunk(index, slot); err != nil {
			t.fail(err)
			return
		}
		slot.release()
	}
}

// claimChunk returns the next unclaimed chunk. Closing exhausted when the last one
// is claimed releases workers still waiting for a slot they no longer need.
func (t *rangedTransfer) claimChunk() (int64, bool) {
	index := t.nextChunk.Add(1) - 1
	if index >= t.chunks-1 {
		t.exhaustedOnce.Do(func() { close(t.exhausted) })
	}

	return index, index < t.chunks
}

// fail records the first error and stops the other workers. Their resulting
// cancellation errors arrive later, so they cannot displace the real cause.
func (t *rangedTransfer) fail(err error) {
	t.failOnce.Do(func() {
		t.failure = err
		t.cancel()
	})
}

// transferChunk fetches one chunk with bounded retries, giving up the slot during
// backoff so a failing host does not hold budget that other downloads could use.
func (t *rangedTransfer) transferChunk(index int64, slot *transferSlot) error {
	start := index * t.chunkSize
	end := min(start+t.chunkSize, t.total) - 1

	for attempt := 1; ; attempt++ {
		var err error
		if index == 0 && attempt == 1 {
			err = t.consumeProbe(end)
		} else {
			err = t.requestChunk(start, end, attempt)
		}
		if err == nil {
			return nil
		}
		// A dead t.ctx means a sibling already failed, so this error is only fallout.
		if ctxErr := t.ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		var writeErr *downloadWriteError
		if errors.As(err, &writeErr) || errors.Is(err, errRangedUnusable) || attempt >= downloadMaxAttempts {
			return err
		}

		delay := downloadRetryDelay(nil, attempt)
		var statusErr *chunkStatusError
		if errors.As(err, &statusErr) {
			delay = statusErr.delay
		}
		slot.release()
		if err := t.waitRetry(min(delay, rangedDownloadMaxRetryDelay)); err != nil {
			return err
		}
		if !slot.acquire(t.ctx, nil) {
			return t.ctx.Err()
		}
	}
}

// waitRetry sleeps for delay unless the transfer is stopped first.
func (t *rangedTransfer) waitRetry(delay time.Duration) error {
	if delay <= 0 {
		return t.ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()

	select {
	case <-t.ctx.Done():
		return t.ctx.Err()
	case <-timer.C:
		return nil
	}
}

// consumeProbe stores the already validated probe body as chunk 0.
func (t *rangedTransfer) consumeProbe(end int64) error {
	defer t.probe.Body.Close()

	return t.copyChunkBody(t.probe.Body, 0, end+1)
}

// requestChunk makes one attempt at a chunk. It always uses the original URL, so a
// signed CDN redirect target that has expired since the probe gets issued again.
func (t *rangedTransfer) requestChunk(start, end int64, attempt int) error {
	request, err := http.NewRequestWithContext(t.ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errRangedUnusable, err)
	}
	setDownloadRequestHeaders(request)
	request.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	request.Header.Set("If-Range", t.validator.value)

	response, err := t.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()

	if err := t.validateChunkResponse(response, start, end, attempt); err != nil {
		return err
	}

	return t.copyChunkBody(response.Body, start, end-start+1)
}

// validateChunkResponse accepts only the exact range of the same file version. A 200
// means If-Range failed or the server stopped honoring Range. 429 and 503 suggest
// that the extra connections are the problem, so they fall back without retrying.
func (t *rangedTransfer) validateChunkResponse(response *http.Response, start, end int64, attempt int) error {
	code := response.StatusCode
	if code != http.StatusPartialContent {
		if retryableDownloadStatus(code) && code != http.StatusTooManyRequests && code != http.StatusServiceUnavailable {
			return &chunkStatusError{status: response.Status, delay: downloadRetryDelay(response, attempt)}
		}
		return fmt.Errorf("%w: chunk %d-%d: status %s", errRangedUnusable, start, end, response.Status)
	}
	if responseIsEncoded(response) {
		return fmt.Errorf("%w: chunk %d-%d: encoded response", errRangedUnusable, start, end)
	}
	gotStart, gotEnd, gotTotal, ok := parseContentRange(response.Header.Get("Content-Range"))
	if !ok || gotStart != start || gotEnd != end || gotTotal != t.total {
		return fmt.Errorf("%w: chunk %d-%d: content range %q", errRangedUnusable, start, end, response.Header.Get("Content-Range"))
	}
	if !t.validator.matches(response) {
		return fmt.Errorf("%w: chunk %d-%d: file changed during download", errRangedUnusable, start, end)
	}

	return nil
}

// copyChunkBody writes one chunk at its offset. A failed attempt subtracts what it
// counted, because the retry will count those bytes again.
func (t *rangedTransfer) copyChunkBody(body io.Reader, start, length int64) error {
	source := io.LimitReader(body, length)
	destination := &chunkWriter{
		writer:   io.NewOffsetWriter(t.file, start),
		reporter: t.reporter,
		path:     t.targetPath,
	}

	written, err := io.Copy(destination, source)
	if err == nil && written < length {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		t.reporter.add(-written)
		return err
	}

	return nil
}

// chunkWriter counts progress and tags failures as local, which lets the retry
// logic tell a full disk apart from a dropped connection.
type chunkWriter struct {
	writer   io.Writer
	reporter *downloadReporter
	path     string
}

func (w *chunkWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.reporter.add(int64(n))
	if err != nil {
		return n, &downloadWriteError{err: fmt.Errorf("write %s: %w", w.path, err)}
	}

	return n, nil
}

// downloadReporter delivers progress from a single goroutine, so that events from
// concurrent workers stay ordered and cannot arrive after the final one.
type downloadReporter struct {
	emit       func(DownloadProgress)
	downloaded atomic.Int64
	total      atomic.Int64
	last       DownloadProgress
	stopping   chan struct{}
	stopped    chan struct{}
}

// startDownloadReporter begins periodic delivery. A nil emit only keeps counters.
func startDownloadReporter(emit func(DownloadProgress)) *downloadReporter {
	reporter := &downloadReporter{emit: emit}
	if emit == nil {
		return reporter
	}
	reporter.stopping = make(chan struct{})
	reporter.stopped = make(chan struct{})

	go func() {
		defer close(reporter.stopped)
		ticker := time.NewTicker(downloadProgressInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				reporter.flush()
			case <-reporter.stopping:
				reporter.flush()
				return
			}
		}
	}()

	return reporter
}

// flush emits the current counts if they changed. Only the delivery goroutine calls
// it, which is why last needs no synchronization.
func (r *downloadReporter) flush() {
	current := DownloadProgress{Downloaded: r.downloaded.Load(), Total: r.total.Load()}
	if current == r.last {
		return
	}
	r.last = current
	r.emit(current)
}

// stop delivers the final counts and waits for delivery to end.
func (r *downloadReporter) stop() {
	if r.emit == nil {
		return
	}
	close(r.stopping)
	<-r.stopped
}

// add adjusts the transferred byte count; negative deltas undo a failed attempt.
func (r *downloadReporter) add(delta int64) {
	r.downloaded.Add(delta)
}

// setTotal records the file size once it is known.
func (r *downloadReporter) setTotal(total int64) {
	r.total.Store(total)
}

// reset clears the counts before a fallback restarts the file from the beginning.
func (r *downloadReporter) reset() {
	r.downloaded.Store(0)
	r.total.Store(0)
}

// Write counts bytes passing through an io.TeeReader on the single-stream path.
func (r *downloadReporter) Write(p []byte) (int, error) {
	r.add(int64(len(p)))
	return len(p), nil
}
