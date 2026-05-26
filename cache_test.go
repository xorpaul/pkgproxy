package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// cachePath returns the on-disk path pkgproxy would use for rawURL.
func cachePath(rawURL string) string {
	parts := strings.SplitN(rawURL, "/", 4)
	return filepath.Join(config.CacheFolder, parts[2], url.QueryEscape(parts[3]))
}

// TestCachingRuleServesFromCacheWhileFresh verifies that a URL matching a
// caching rule is served from cache on the second request without hitting the
// remote, as long as the rule's TTL has not expired.
func TestCachingRuleServesFromCacheWhileFresh(t *testing.T) {
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		fmt.Fprint(w, "package-content")
	}))
	defer backend.Close()

	config.CacheRules = map[string]CachingRules{
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "1h", TTL: time.Hour, CompiledRegex: regexp.MustCompile(`.*\.rpm$`)},
	}
	t.Cleanup(func() { config.CacheRules = nil })

	path := "/" + strings.TrimPrefix(backend.URL, "http://") + "/package.rpm"

	// First request: cache miss, must fetch from remote.
	w1 := httptest.NewRecorder()
	handleGet(w1, httptest.NewRequest("GET", path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", w1.Code)
	}
	if n := backendCalls.Load(); n != 1 {
		t.Fatalf("first request: want 1 backend call, got %d", n)
	}

	// Second request: rule TTL is 1h, cache is fresh, remote must not be called again.
	w2 := httptest.NewRecorder()
	handleGet(w2, httptest.NewRequest("GET", path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second request: want 200, got %d", w2.Code)
	}
	if n := backendCalls.Load(); n != 1 {
		t.Errorf("second request: cache should be used, want 1 backend call total, got %d", n)
	}
}

// TestCachingRuleRefreshesAfterExpiry verifies that once a caching rule's TTL
// has elapsed the item is re-fetched from the remote.
func TestCachingRuleRefreshesAfterExpiry(t *testing.T) {
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		fmt.Fprint(w, "package-content")
	}))
	defer backend.Close()

	config.CacheRules = map[string]CachingRules{
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "30m", TTL: 30 * time.Minute, CompiledRegex: regexp.MustCompile(`.*\.rpm$`)},
	}
	t.Cleanup(func() { config.CacheRules = nil })

	fullURL := "http://" + strings.TrimPrefix(backend.URL, "http://") + "/package.rpm"
	path := "/" + strings.TrimPrefix(fullURL, "http://")

	// First request: cache miss.
	w1 := httptest.NewRecorder()
	handleGet(w1, httptest.NewRequest("GET", path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", w1.Code)
	}
	if n := backendCalls.Load(); n != 1 {
		t.Fatalf("first request: want 1 backend call, got %d", n)
	}

	// Age the cached file to 1 hour ago so the 30-minute TTL is exceeded.
	stale := time.Now().Add(-time.Hour)
	if err := os.Chtimes(cachePath(fullURL), stale, stale); err != nil {
		t.Fatalf("could not age cache file: %v", err)
	}

	// Second request: TTL exceeded, must re-fetch from remote.
	w2 := httptest.NewRecorder()
	handleGet(w2, httptest.NewRequest("GET", path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second request: want 200, got %d", w2.Code)
	}
	if n := backendCalls.Load(); n != 2 {
		t.Errorf("second request: want 2 backend calls (TTL expired), got %d", n)
	}
}

// TestDefaultCacheTTLAppliedWhenNoRuleMatches verifies that when no caching
// rule regex matches the requested URL the default TTL is used instead of any
// rule's TTL.
func TestDefaultCacheTTLAppliedWhenNoRuleMatches(t *testing.T) {
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		fmt.Fprint(w, "content")
	}))
	defer backend.Close()

	// Rule only matches .rpm; request is for .txt, so default TTL (1h) applies.
	config.CacheRules = map[string]CachingRules{
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "1ms", TTL: time.Millisecond, CompiledRegex: regexp.MustCompile(`.*\.rpm$`)},
	}
	t.Cleanup(func() { config.CacheRules = nil })

	fullURL := "http://" + strings.TrimPrefix(backend.URL, "http://") + "/readme.txt"
	path := "/" + strings.TrimPrefix(fullURL, "http://")

	// First request: cache miss.
	w1 := httptest.NewRecorder()
	handleGet(w1, httptest.NewRequest("GET", path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", w1.Code)
	}

	// Age the file to 5 minutes ago. The 1ms .rpm rule would have expired but
	// .txt doesn't match it; the default 1h TTL is still fresh.
	stale := time.Now().Add(-5 * time.Minute)
	if err := os.Chtimes(cachePath(fullURL), stale, stale); err != nil {
		t.Fatalf("could not age cache file: %v", err)
	}

	// Second request: .txt misses the 1ms rule, default 1h TTL still fresh.
	// Remote must not be called again.
	w2 := httptest.NewRecorder()
	handleGet(w2, httptest.NewRequest("GET", path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second request: want 200, got %d", w2.Code)
	}
	if n := backendCalls.Load(); n != 1 {
		t.Errorf("second request: want 1 backend call (default TTL, still fresh), got %d", n)
	}
}

// TestCachePutFlushesCompleteContent verifies that cache.put writes the full
// content to disk. bufio.Writer buffers writes internally; without an explicit
// Flush() the trailing partial buffer is never written to the file. We use a
// payload larger than bufio's default 4096-byte buffer so the final chunk
// stays buffered until Flush() forces it out.
func TestCachePutFlushesCompleteContent(t *testing.T) {
	const size = 8192
	content := strings.Repeat("x", size)
	r := strings.NewReader(content)
	var reader io.Reader = r

	rawURL := "http://flush-test.example.com/bigfile.bin"
	if err := cache.put(rawURL, &reader, int64(size)); err != nil {
		t.Fatalf("cache.put: %v", err)
	}

	// Reconstruct the on-disk path using the same logic as cache.put.
	parts := strings.SplitN(rawURL, "/", 4)
	cacheFile := filepath.Join(config.CacheFolder, parts[2], url.QueryEscape(parts[3]))

	data, err := os.ReadFile(cacheFile)
	if err != nil {
		t.Fatalf("reading cache file from disk: %v", err)
	}
	if len(data) != size {
		t.Errorf("cache file size on disk: want %d bytes, got %d (trailing buffer not flushed?)", size, len(data))
	}
	if string(data) != content {
		t.Error("cache file content does not match written content")
	}
}

// TestCreateCachePrefillsBothHTTPAndHTTPS verifies that when
// prefill_cache_on_startup is true, CreateCache indexes files from both the
// HTTP cache dir and the HTTPS cache dir. This covers the code path where two
// concurrent WalkDir goroutines both complete and their results are both
// awaited before CreateCache returns.
func TestCreateCachePrefillsBothHTTPAndHTTPS(t *testing.T) {
	tmpDir := t.TempDir()
	httpDir := filepath.Join(tmpDir, "http")
	httpsDir := filepath.Join(tmpDir, "https")

	// Create one cached file under each scheme dir, mirroring the directory
	// structure that cache.put writes: <cacheDir>/<host>/<encoded-path>.
	if err := os.MkdirAll(filepath.Join(httpDir, "pkg.example.com"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(httpDir, "pkg.example.com", "file.rpm"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(httpsDir, "secure.example.com"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(httpsDir, "secure.example.com", "file.deb"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}

	oldHTTP := config.CacheFolder
	oldHTTPS := config.CacheFolderHTTPS
	oldPrefill := config.PrefillCacheOnStartup
	config.CacheFolder = httpDir
	config.CacheFolderHTTPS = httpsDir
	config.PrefillCacheOnStartup = true
	t.Cleanup(func() {
		config.CacheFolder = oldHTTP
		config.CacheFolderHTTPS = oldHTTPS
		config.PrefillCacheOnStartup = oldPrefill
	})

	c, err := CreateCache()
	if err != nil {
		t.Fatalf("CreateCache: %v", err)
	}

	c.mutex.Lock()
	_, hasHTTP := c.cacheMemoryItems["http://pkg.example.com/file.rpm"]
	_, hasHTTPS := c.cacheMemoryItems["https://secure.example.com/file.deb"]
	c.mutex.Unlock()

	if !hasHTTP {
		t.Error("HTTP cache file not indexed by CreateCache")
	}
	if !hasHTTPS {
		t.Error("HTTPS cache file not indexed by CreateCache")
	}
}

// TestConditionalGet304AvoidRedownload verifies that when a cached item's TTL
// has expired but the upstream returns 304 Not Modified, pkgproxy revalidates
// the cache without re-downloading the body, resets the cache file mtime, and
// serves subsequent requests from cache without hitting the backend again.
func TestConditionalGet304AvoidRedownload(t *testing.T) {
	const content = "cached-content"
	const etag = `"revalidate-etag-abc"`

	var lastRequestHeaders http.Header
	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		lastRequestHeaders = r.Header.Clone()
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Write([]byte(content))
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/conditional-304-test"
	fullURL := "http://" + hostAndPath
	path := "/" + hostAndPath

	// Prime the cache.
	w1 := httptest.NewRecorder()
	handleGet(w1, httptest.NewRequest("GET", path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("initial GET: want 200, got %d", w1.Code)
	}
	if callCount != 1 {
		t.Fatalf("initial GET: want 1 backend call, got %d", callCount)
	}

	// Verify meta sidecar was written with the ETag.
	cf, err := cacheFilePath(fullURL)
	if err != nil {
		t.Fatalf("cacheFilePath: %v", err)
	}
	meta := loadMeta(cf)
	if meta.ETag != etag {
		t.Errorf("meta ETag after initial GET: want %q, got %q", etag, meta.ETag)
	}

	// Age the cache file past the default TTL (1h) to force revalidation.
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cf, stale, stale); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	// Second GET: TTL expired, conditional GET should yield 304 — no body re-download.
	w2 := httptest.NewRecorder()
	handleGet(w2, httptest.NewRequest("GET", path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second GET: want 200, got %d", w2.Code)
	}
	if callCount != 2 {
		t.Fatalf("second GET: want 2 backend calls total, got %d", callCount)
	}
	if lastRequestHeaders.Get("If-None-Match") != etag {
		t.Errorf("second GET: If-None-Match not sent (got %q)", lastRequestHeaders.Get("If-None-Match"))
	}

	// Cache file mtime should be reset to approximately now.
	fi, err := os.Stat(cf)
	if err != nil {
		t.Fatalf("stat cache file: %v", err)
	}
	if time.Since(fi.ModTime()) > 5*time.Second {
		t.Errorf("cache file mtime not reset after 304 (mtime %v)", fi.ModTime())
	}

	// Third GET within the fresh TTL: must not hit backend again.
	w3 := httptest.NewRecorder()
	handleGet(w3, httptest.NewRequest("GET", path, nil))
	if w3.Code != http.StatusOK {
		t.Fatalf("third GET: want 200, got %d", w3.Code)
	}
	if callCount != 2 {
		t.Errorf("third GET: want still 2 backend calls (TTL reset by 304), got %d", callCount)
	}
}

// TestConditionalGet200UpdatesCache verifies that when the upstream returns 200
// on a conditional GET (content changed), pkgproxy updates the cached body and
// the meta sidecar, and serves the new content on the next request.
func TestConditionalGet200UpdatesCache(t *testing.T) {
	const content1 = "original-content"
	const content2 = "updated-content"
	const etag1 = `"etag-v1"`
	const etag2 = `"etag-v2"`

	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if r.Header.Get("If-None-Match") == etag1 {
			// Content changed: send new version with new ETag.
			w.Header().Set("ETag", etag2)
			w.Write([]byte(content2))
			return
		}
		w.Header().Set("ETag", etag1)
		w.Write([]byte(content1))
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/conditional-200-test"
	fullURL := "http://" + hostAndPath
	path := "/" + hostAndPath

	// Prime the cache.
	w1 := httptest.NewRecorder()
	handleGet(w1, httptest.NewRequest("GET", path, nil))
	if w1.Code != http.StatusOK {
		t.Fatalf("initial GET: want 200, got %d", w1.Code)
	}

	cf, err := cacheFilePath(fullURL)
	if err != nil {
		t.Fatalf("cacheFilePath: %v", err)
	}

	// Age the cache file to force TTL expiry.
	stale := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(cf, stale, stale); err != nil {
		t.Fatalf("os.Chtimes: %v", err)
	}

	// Second GET: TTL expired, conditional GET returns 200 (content changed).
	w2 := httptest.NewRecorder()
	handleGet(w2, httptest.NewRequest("GET", path, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("second GET: want 200, got %d", w2.Code)
	}
	if callCount != 2 {
		t.Fatalf("second GET: want 2 backend calls total, got %d", callCount)
	}

	body, _ := io.ReadAll(w2.Result().Body)
	if string(body) != content2 {
		t.Errorf("second GET body: want %q, got %q", content2, string(body))
	}

	// Meta sidecar should have the updated ETag.
	meta := loadMeta(cf)
	if meta.ETag != etag2 {
		t.Errorf("meta ETag after update: want %q, got %q", etag2, meta.ETag)
	}
}
