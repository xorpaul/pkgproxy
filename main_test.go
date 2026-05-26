package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

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

	config = &Config{
		CacheFolder:            cacheDir,
		CacheFolderHTTPS:       cacheHTTPSDir,
		DefaultCacheTTL:        time.Hour,
		MaxCacheItemSize:       100,
		Timeout:                5,
		PrometheusMetricPrefix: "test_",
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
