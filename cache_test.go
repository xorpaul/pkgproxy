package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
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
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "1h", TTL: time.Hour},
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
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "30m", TTL: 30 * time.Minute},
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
		"RPM Packages": {Regex: `.*\.rpm$`, TTLString: "1ms", TTL: time.Millisecond},
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
