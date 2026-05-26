package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)


func mustCompileRegex(s string) *regexp.Regexp {
	return regexp.MustCompile(s)
}

func TestMain(m *testing.M) {
	tmpDir, err := os.MkdirTemp("", "pkgproxy-test-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cacheDir := filepath.Join(tmpDir, "cache")
	cacheHTTPSDir := filepath.Join(tmpDir, "cache_https")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(cacheHTTPSDir, 0755); err != nil {
		log.Fatal(err)
	}

	compiledMetadataPatterns := make([]*regexp.Regexp, 0, len(defaultMetadataPatternStrings))
	for _, p := range defaultMetadataPatternStrings {
		compiledMetadataPatterns = append(compiledMetadataPatterns, regexp.MustCompile(p))
	}

	config = &Config{
		CacheFolder:              cacheDir,
		CacheFolderHTTPS:         cacheHTTPSDir,
		DefaultCacheTTL:          time.Hour,
		MaxCacheItemSize:         100,
		Timeout:                  5,
		PrometheusMetricPrefix:   "test_",
		CompiledMetadataPatterns: compiledMetadataPatterns,
	}

	client = &http.Client{Timeout: 5 * time.Second}

	cache, err = CreateCache()
	if err != nil {
		log.Fatal(err)
	}

	initTestMetrics()

	os.Exit(m.Run())
}

func initTestMetrics() {
	promCounters = make(map[string]prometheus.Counter)
	for _, name := range []string{
		"TOTAL_REQUESTS", "REMOTE_ERRORS", "REMOTE_OK",
		"TOTAL_HTTP_NONGET_REQUESTS", "TOTAL_HTTP_REQUESTS", "TOTAL_HTTPS_REQUESTS",
		"CACHE_HIT", "CACHE_MISS", "CACHE_INVALIDATE", "CACHE_TOO_OLD",
		"CACHE_OK", "CACHE_ITEM_MISSING",
		"NEGATIVE_CACHE_HIT", "NEGATIVE_CACHE_PUT", "NEGATIVE_CACHE_INVALIDATE",
		"CACHE_PREFIX_INVALIDATE",
	} {
		promCounters[name] = prometheus.NewCounter(prometheus.CounterOpts{Name: "test_noop"})
	}
	promSummaries = make(map[string]prometheus.Summary)
	for _, name := range []string{"CACHE_READ_MEMORY", "CACHE_READ_FILE"} {
		promSummaries[name] = prometheus.NewSummary(prometheus.SummaryOpts{Name: "test_noop"})
	}
}

// TestHandleGetUnreachableHost exercises the full path through handleGet when
// the remote cannot be reached (connection refused), verifying the error header and body.
func TestHandleGetUnreachableHost(t *testing.T) {
	// Start then immediately close a server so the port is not listening.
	// A local address is used so the connection refused error comes back
	// directly rather than being intercepted by any transparent proxy.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close()

	req := httptest.NewRequest("GET", "/"+addr+"/some/path", nil)
	w := httptest.NewRecorder()
	handleGet(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status: want 500, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Pkgproxy-Error"); got != "remote-unreachable" {
		t.Errorf("X-Pkgproxy-Error: want %q, got %q", "remote-unreachable", got)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "pkgproxy: could not reach the requested remote URL") {
		t.Errorf("body missing helpful message, got: %s", body)
	}
}

// TestPatchNonCachedItem verifies that a PATCH request for an item not yet in
// the cache returns 404 without attempting a download.
func TestPatchNonCachedItem(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("backend should not be contacted for PATCH on non-cached item")
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/never-cached-file"
	req := httptest.NewRequest("PATCH", "/"+hostAndPath, nil)
	w := httptest.NewRecorder()
	handleGet(w, req)

	resp := w.Result()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", resp.StatusCode)
	}
}

// TestPatchCachedItem verifies that a PATCH request for an item already in the
// cache triggers a re-download (cache invalidation) and returns 200. A
// subsequent GET must return the refreshed content.
func TestPatchCachedItem(t *testing.T) {
	const firstContent = "original content"
	const secondContent = "refreshed content"
	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.Write([]byte(firstContent))
		} else {
			w.Write([]byte(secondContent))
		}
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/patch-test-file"

	// Prime the cache with a GET.
	getReq := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	getW := httptest.NewRecorder()
	handleGet(getW, getReq)
	if getW.Result().StatusCode != http.StatusOK {
		t.Fatalf("initial GET status: want 200, got %d", getW.Result().StatusCode)
	}

	// PATCH should invalidate and trigger a re-download (2nd backend call).
	patchReq := httptest.NewRequest("PATCH", "/"+hostAndPath, nil)
	patchW := httptest.NewRecorder()
	handleGet(patchW, patchReq)

	if patchW.Result().StatusCode != http.StatusOK {
		t.Fatalf("PATCH status: want 200, got %d", patchW.Result().StatusCode)
	}
	if callCount != 2 {
		t.Errorf("backend call count after PATCH: want 2, got %d", callCount)
	}

	// A subsequent GET must serve the refreshed content from cache without
	// hitting the backend again.
	getReq2 := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	getW2 := httptest.NewRecorder()
	handleGet(getW2, getReq2)
	body, _ := io.ReadAll(getW2.Result().Body)
	if !strings.Contains(string(body), secondContent) {
		t.Errorf("GET after PATCH: want %q in body, got %q", secondContent, string(body))
	}
	if callCount != 2 {
		t.Errorf("backend call count after GET: want still 2, got %d", callCount)
	}
}

// TestHandleGetRemoteNonOKStatus exercises the path where the remote server
// is reachable but returns a non-200 status, verifying the error header.
func TestHandleGetRemoteNonOKStatus(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream not found", http.StatusNotFound)
	}))
	defer backend.Close()

	// Strip scheme so the host:port becomes the first path segment,
	// which handleGet interprets as the remote host to proxy to.
	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/testfile"
	req := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	w := httptest.NewRecorder()
	handleGet(w, req)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status: want 404, got %d", resp.StatusCode)
	}
	want := "remote returned HTTP 404"
	if got := resp.Header.Get("X-Pkgproxy-Error"); got != want {
		t.Errorf("X-Pkgproxy-Error: want %q, got %q", want, got)
	}
}

// TestNegativeCacheStopsUpstreamHits verifies that after a 404 is stored in
// the negative cache, subsequent requests are served from the cache without
// contacting upstream, and that the correct response headers are set.
func TestNegativeCacheStopsUpstreamHits(t *testing.T) {
	origTTL := config.NegativeCacheTTL
	config.NegativeCacheTTL = time.Hour
	defer func() { config.NegativeCacheTTL = origTTL }()

	hitCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/missing-file"

	// First request: cache miss, hits upstream, stores negative cache entry.
	// Must get X-Pkgproxy-Error and X-Pkgproxy-Cache-Expires.
	before := time.Now()
	req1 := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	w1 := httptest.NewRecorder()
	handleGet(w1, req1)
	resp1 := w1.Result()
	if resp1.StatusCode != http.StatusNotFound {
		t.Fatalf("first request: want 404, got %d", resp1.StatusCode)
	}
	if hitCount != 1 {
		t.Fatalf("first request: want 1 upstream hit, got %d", hitCount)
	}
	if got := resp1.Header.Get("X-Pkgproxy-Error"); got != "remote returned HTTP 404" {
		t.Errorf("first request X-Pkgproxy-Error: want %q, got %q", "remote returned HTTP 404", got)
	}
	expiresStr := resp1.Header.Get("X-Pkgproxy-Cache-Expires")
	if expiresStr == "" {
		t.Error("first request: X-Pkgproxy-Cache-Expires header missing")
	} else {
		expiry, err := time.Parse(time.RFC3339, expiresStr)
		if err != nil {
			t.Errorf("first request: X-Pkgproxy-Cache-Expires not RFC3339: %s", expiresStr)
		} else if expiry.Before(before.Add(59 * time.Minute)) {
			t.Errorf("first request: expiry %s is earlier than expected (want ~1h from now)", expiresStr)
		}
	}

	// Second request: must be served from negative cache, no upstream hit.
	// Must get X-Pkgproxy-Negative-Cache: HIT and X-Pkgproxy-Cache-Expires.
	req2 := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	w2 := httptest.NewRecorder()
	handleGet(w2, req2)
	resp2 := w2.Result()
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("second request: want 404, got %d", resp2.StatusCode)
	}
	if hitCount != 1 {
		t.Errorf("second request hit upstream again (want still 1, got %d)", hitCount)
	}
	if got := resp2.Header.Get("X-Pkgproxy-Negative-Cache"); got != "HIT" {
		t.Errorf("second request X-Pkgproxy-Negative-Cache: want %q, got %q", "HIT", got)
	}
	if resp2.Header.Get("X-Pkgproxy-Cache-Expires") == "" {
		t.Error("second request: X-Pkgproxy-Cache-Expires header missing")
	}
}

// TestNegativeCachePatchInvalidates verifies that a PATCH request clears the
// negative cache entry so the next GET re-fetches from upstream.
func TestNegativeCachePatchInvalidates(t *testing.T) {
	origTTL := config.NegativeCacheTTL
	config.NegativeCacheTTL = time.Hour
	defer func() { config.NegativeCacheTTL = origTTL }()

	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			http.Error(w, "not found", http.StatusNotFound)
		} else {
			w.Write([]byte("now it exists"))
		}
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/appears-later"

	// Prime the negative cache.
	req1 := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	handleGet(httptest.NewRecorder(), req1)
	if callCount != 1 {
		t.Fatalf("prime: want 1 upstream call, got %d", callCount)
	}
	if found, _ := cache.isNotFound("http://" + hostAndPath); !found {
		t.Fatal("expected negative cache entry after first 404")
	}

	// PATCH should invalidate the negative cache entry.
	reqPatch := httptest.NewRequest("PATCH", "/"+hostAndPath, nil)
	wPatch := httptest.NewRecorder()
	handleGet(wPatch, reqPatch)
	if found, _ := cache.isNotFound("http://" + hostAndPath); found {
		t.Error("negative cache entry should be cleared after PATCH")
	}

	// Next GET must re-fetch (entry is now available upstream).
	req2 := httptest.NewRequest("GET", "/"+hostAndPath, nil)
	w2 := httptest.NewRecorder()
	handleGet(w2, req2)
	if w2.Result().StatusCode != http.StatusOK {
		t.Fatalf("GET after PATCH: want 200, got %d", w2.Result().StatusCode)
	}
	if callCount != 2 {
		t.Errorf("GET after PATCH: want 2 upstream calls, got %d", callCount)
	}
}

// TestNegativeCacheTTLExpiry verifies that an expired negative cache entry
// allows upstream to be contacted again.
func TestNegativeCacheTTLExpiry(t *testing.T) {
	origTTL := config.NegativeCacheTTL
	config.NegativeCacheTTL = 1 * time.Millisecond
	defer func() { config.NegativeCacheTTL = origTTL }()

	hitCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/ttl-test-file"

	// First request: stores negative cache entry.
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+hostAndPath, nil))
	if hitCount != 1 {
		t.Fatalf("first request: want 1 upstream hit, got %d", hitCount)
	}

	// Wait for the TTL to expire.
	time.Sleep(5 * time.Millisecond)

	// Second request: TTL expired, must hit upstream again.
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+hostAndPath, nil))
	if hitCount != 2 {
		t.Errorf("after TTL expiry: want 2 upstream hits, got %d", hitCount)
	}
}

// TestDeleteInvalidatesMetadataFiles verifies that a DELETE request removes
// cached metadata files under the given prefix while leaving package binaries
// untouched. It also checks the X-Pkgproxy-Invalidated response header.
func TestDeleteInvalidatesMetadataFiles(t *testing.T) {
	callCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Write([]byte("content"))
	}))
	defer backend.Close()

	host := strings.TrimPrefix(backend.URL, "http://")
	metaPath := host + "/dists/focal/Packages"
	pkgPath := host + "/pool/main/p/pkg/pkg_1.0_amd64.deb"

	// Prime the cache with a metadata file and a package file.
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+metaPath, nil))
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+pkgPath, nil))
	if callCount != 2 {
		t.Fatalf("priming: want 2 backend calls, got %d", callCount)
	}

	// Verify both are in the memory cache.
	if _, ok := cache.cacheMemoryItems["http://"+metaPath]; !ok {
		t.Fatal("metadata file not in cache after GET")
	}
	if _, ok := cache.cacheMemoryItems["http://"+pkgPath]; !ok {
		t.Fatal("package file not in cache after GET")
	}

	// DELETE the dists/ prefix — should remove Packages but not the .deb.
	delReq := httptest.NewRequest("DELETE", "/"+host+"/dists/", nil)
	delW := httptest.NewRecorder()
	handleGet(delW, delReq)

	delResp := delW.Result()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE status: want 200, got %d", delResp.StatusCode)
	}
	if got := delResp.Header.Get("X-Pkgproxy-Invalidated"); got != "1" {
		t.Errorf("X-Pkgproxy-Invalidated: want %q, got %q", "1", got)
	}

	// Metadata file must be gone from memory cache.
	if _, ok := cache.cacheMemoryItems["http://"+metaPath]; ok {
		t.Error("metadata file still in memory cache after DELETE")
	}
	// Package file must still be cached.
	if _, ok := cache.cacheMemoryItems["http://"+pkgPath]; !ok {
		t.Error("package file was incorrectly removed from cache by DELETE")
	}

	// Next GET for the metadata file must hit backend again (cache miss).
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+metaPath, nil))
	if callCount != 3 {
		t.Errorf("GET after DELETE: want 3 backend calls (re-fetch), got %d", callCount)
	}
}

// TestDeleteInvalidatesNegativeCacheEntries verifies that DELETE also clears
// negative cache (.404) entries for metadata files under the given prefix.
func TestDeleteInvalidatesNegativeCacheEntries(t *testing.T) {
	origTTL := config.NegativeCacheTTL
	config.NegativeCacheTTL = time.Hour
	defer func() { config.NegativeCacheTTL = origTTL }()

	hitCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer backend.Close()

	host := strings.TrimPrefix(backend.URL, "http://")
	metaPath := host + "/repodata/repomd.xml"

	// Prime the negative cache.
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+metaPath, nil))
	if hitCount != 1 {
		t.Fatalf("prime: want 1 upstream call, got %d", hitCount)
	}
	if found, _ := cache.isNotFound("http://" + metaPath); !found {
		t.Fatal("expected negative cache entry after 404")
	}

	// DELETE the repodata/ prefix — should clear the negative cache entry.
	delReq := httptest.NewRequest("DELETE", "/"+host+"/repodata/", nil)
	handleGet(httptest.NewRecorder(), delReq)

	if found, _ := cache.isNotFound("http://" + metaPath); found {
		t.Error("negative cache entry still present after DELETE")
	}
}

// TestRangeAndContentLength verifies that the proxy sets Content-Length and
// Accept-Ranges on normal responses, returns 206 Partial Content with correct
// headers for Range requests, and returns 416 for unsatisfiable ranges.
// It covers both the in-memory cache path (small file) and the on-disk path
// (file larger than MaxCacheItemSize that is only served from os.File).
func TestRangeAndContentLength(t *testing.T) {
	const content = "0123456789abcdefghij" // 20 bytes

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(content))
	}))
	defer backend.Close()

	for _, tc := range []struct {
		name            string
		maxCacheItemMB  int64 // controls whether content lands in memory or only on disk
	}{
		{"memory-cached", 100},
		{"disk-only", 0}, // MaxCacheItemSize=0 MB → content never fits in memory
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Each sub-test needs an independent cache so items from the other
			// sub-test don't bleed through.
			origMax := config.MaxCacheItemSize
			config.MaxCacheItemSize = tc.maxCacheItemMB
			defer func() { config.MaxCacheItemSize = origMax }()

			hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/range-test-" + tc.name

			// ── prime the cache ──────────────────────────────────────────────
			getReq := httptest.NewRequest("GET", "/"+hostAndPath, nil)
			getW := httptest.NewRecorder()
			handleGet(getW, getReq)
			if getW.Result().StatusCode != http.StatusOK {
				t.Fatalf("initial GET: want 200, got %d", getW.Result().StatusCode)
			}

			// ── Content-Length and Accept-Ranges on normal GET ───────────────
			if cl := getW.Result().ContentLength; cl != int64(len(content)) {
				t.Errorf("Content-Length: want %d, got %d", len(content), cl)
			}
			if ar := getW.Result().Header.Get("Accept-Ranges"); ar != "bytes" {
				t.Errorf("Accept-Ranges: want %q, got %q", "bytes", ar)
			}

			// ── Range request: bytes 5-14 (10 bytes) ────────────────────────
			rangeReq := httptest.NewRequest("GET", "/"+hostAndPath, nil)
			rangeReq.Header.Set("Range", "bytes=5-14")
			rangeW := httptest.NewRecorder()
			handleGet(rangeW, rangeReq)

			resp := rangeW.Result()
			if resp.StatusCode != http.StatusPartialContent {
				t.Fatalf("Range request: want 206, got %d", resp.StatusCode)
			}
			const wantRange = "bytes 5-14/20"
			if got := resp.Header.Get("Content-Range"); got != wantRange {
				t.Errorf("Content-Range: want %q, got %q", wantRange, got)
			}
			body, _ := io.ReadAll(resp.Body)
			if string(body) != content[5:15] {
				t.Errorf("partial body: want %q, got %q", content[5:15], string(body))
			}
			if cl := resp.ContentLength; cl != 10 {
				t.Errorf("Content-Length for range: want 10, got %d", cl)
			}

			// ── unsatisfiable range returns 416 ──────────────────────────────
			oobReq := httptest.NewRequest("GET", "/"+hostAndPath, nil)
			oobReq.Header.Set("Range", "bytes=100-200")
			oobW := httptest.NewRecorder()
			handleGet(oobW, oobReq)
			if oobW.Result().StatusCode != http.StatusRequestedRangeNotSatisfiable {
				t.Errorf("out-of-range: want 416, got %d", oobW.Result().StatusCode)
			}
		})
	}
}

// TestNegativeCacheRuleOverridesTTL verifies that a per-URL rule in
// NegativeCacheRules takes precedence over the default NegativeCacheTTL.
func TestNegativeCacheRuleOverridesTTL(t *testing.T) {
	origTTL := config.NegativeCacheTTL
	origRules := config.NegativeCacheRules
	config.NegativeCacheTTL = time.Hour
	config.NegativeCacheRules = map[string]CachingRules{
		"short-ttl-rule": {
			Regex:         ".*short-ttl.*",
			TTL:           1 * time.Millisecond,
			CompiledRegex: mustCompileRegex(".*short-ttl.*"),
		},
	}
	defer func() {
		config.NegativeCacheTTL = origTTL
		config.NegativeCacheRules = origRules
	}()

	hitCount := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitCount++
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer backend.Close()

	hostAndPath := strings.TrimPrefix(backend.URL, "http://") + "/short-ttl-resource"

	// First request: stores negative cache entry with 1ms TTL (from rule).
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+hostAndPath, nil))
	if hitCount != 1 {
		t.Fatalf("first request: want 1 upstream hit, got %d", hitCount)
	}

	// Wait for the rule TTL (1ms) to expire, default (1h) has not expired.
	time.Sleep(5 * time.Millisecond)

	// Second request: rule TTL expired, must hit upstream again.
	handleGet(httptest.NewRecorder(), httptest.NewRequest("GET", "/"+hostAndPath, nil))
	if hitCount != 2 {
		t.Errorf("after rule TTL expiry: want 2 upstream hits, got %d", hitCount)
	}
}
