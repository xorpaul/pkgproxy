package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	h "github.com/xorpaul/gohelper"
	olo "github.com/xorpaul/sigolo"
)

var (
	debug        bool
	verbose      bool
	buildtime    string
	buildversion string

	config *Config
	cache  *Cache

	client *http.Client

	promCounters  map[string]prometheus.Counter
	promSummaries map[string]prometheus.Summary
)

func main() {

	var (
		configFileFlag = flag.String("config", "example.yaml", "which config file to use")
		versionFlag    = flag.Bool("version", false, "show build time and version number")
	)
	flag.BoolVar(&debug, "debug", false, "log debug output, defaults to false")
	flag.BoolVar(&verbose, "verbose", false, "log verbose output, defaults to false")
	flag.Parse()

	configFile := *configFileFlag
	version := *versionFlag

	if version {
		fmt.Println("pkgproxy", buildversion, " Build time:", buildtime, "UTC")
		os.Exit(0)
	}

	loadConfig(configFile)

	if config.Debug || debug {
		olo.LogLevel = olo.LOG_DEBUG
	}
	olo.Debug("Config loaded")

	if config.ListenPort == 0 && config.ListenSSLPort == 0 {
		olo.Fatal("Both listen_port and list_ssl_port set to 0, need to enable at least one")
	}

	prepare()
	olo.Debug("Cache initialized")

	if config.ListenPort > 0 {
		go serve()
	}
	if config.ListenSSLPort > 0 {
		if config.NoSSL {
			olo.Info("Not starting HTTPS server as no_ssl is set to true")
		} else {
			go serveTLS()
		}
	}

	// prometheus metrics server
	http.Handle("/metrics", promhttp.Handler())

	// Set default values if not configured
	prometheusAddress := config.ListenAddressPrometheus
	if prometheusAddress == "" {
		prometheusAddress = "127.0.0.1"
	}
	prometheusPort := config.ListenPortPrometheus
	if prometheusPort == 0 {
		prometheusPort = 2112
	}

	prometheusListenAddr := fmt.Sprintf("%s:%d", prometheusAddress, prometheusPort)
	olo.Info("Listening on http://%s/metrics", prometheusListenAddr)
	err := http.ListenAndServe(prometheusListenAddr, nil)
	olo.Fatal("Error starting metrics server: %s", err.Error())
}

func loadConfig(configFile string) {
	var err error

	config, err = LoadConfig(configFile)
	if err != nil {
		olo.Fatal("Could not read config %s: '%s'", configFile, err.Error())
	}

}

func loggingDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	localAddr := conn.LocalAddr().String()
	remoteAddr := conn.RemoteAddr().String()
	olo.Info("Established connection from %s to %s for %s", localAddr, remoteAddr, addr)
	return conn, nil
}

func prepare() {
	var err error

	cache, err = CreateCache()
	if err != nil {
		olo.Fatal("Could not init cache: '%s'", err.Error())
	}

	// https://medium.com/@nate510/don-t-use-go-s-default-http-client-4804cb19f779
	// fixes unresponsive sockets that lead to too many open files errors and service outage
	var netTransport = &http.Transport{
		DialContext:         loggingDialContext,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if len(config.Proxy) > 0 {
		netTransport.Proxy = http.ProxyURL(config.ProxyURL)
	}

	// No Client.Timeout: it limits the total time of a request including the
	// body download and would cut off every large download that takes longer.
	// doWithStallTimeout aborts stalled upstream connections instead.
	client = &http.Client{
		Transport: netTransport,
	}

	promCounters = make(map[string]prometheus.Counter)
	promCounters["TOTAL_REQUESTS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_requests_total",
		Help: "The total number of requests",
	})
	promCounters["REMOTE_ERRORS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_remote_errors_total",
		Help: "The total number of remote requests that were unsuccessfull",
	})
	promCounters["REMOTE_OK"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_remote_ok_total",
		Help: "The total number of remote requests that were successfull",
	})
	promCounters["TOTAL_HTTP_NONGET_REQUESTS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_http_nonget_requests_total",
		Help: "The total number of non HTTP GET requests, like POST PUT etc",
	})
	promCounters["TOTAL_HTTP_REQUESTS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_http_requests_total",
		Help: "The total number of HTTP requests",
	})
	promCounters["TOTAL_HTTPS_REQUESTS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_https_requests_total",
		Help: "The total number of HTTPS requests",
	})
	promCounters["CACHE_HIT"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_hit_total",
		Help: "The total number of requests that were already cached",
	})
	promCounters["CACHE_MISS"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_miss_total",
		Help: "The total number of requests were no cache was found",
	})
	promCounters["CACHE_INVALIDATE"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_invalidation_total",
		Help: "The total number of PATCHrequests were the cached item was forced to be invalidated",
	})
	promCounters["CACHE_TOO_OLD"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_old_total",
		Help: "The total number of requests that were already cached, but the cache was too old and needed to be renewed",
	})
	promCounters["CACHE_OK"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_ok_total",
		Help: "The total number of requests that were already cached and the cache was not too old",
	})
	promCounters["CACHE_ITEM_MISSING"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_item_missing_total",
		Help: "Cache item was known while starting the service, but was removed afterwards, this should really be 0 otherwise something is seriously wrong",
	})
	promCounters["NEGATIVE_CACHE_HIT"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_negative_cache_hit_total",
		Help: "The total number of requests served directly from negative cache (upstream returned 404 previously)",
	})
	promCounters["NEGATIVE_CACHE_PUT"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_negative_cache_put_total",
		Help: "The total number of 404 responses stored in the negative cache",
	})
	promCounters["NEGATIVE_CACHE_INVALIDATE"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_negative_cache_invalidate_total",
		Help: "The total number of negative cache entries cleared by PATCH requests",
	})
	promCounters["CACHE_PREFIX_INVALIDATE"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_prefix_invalidation_total",
		Help: "The total number of DELETE requests that performed metadata cache invalidation",
	})
	promCounters["CACHE_REVALIDATED"] = promauto.NewCounter(prometheus.CounterOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_revalidated_total",
		Help: "The total number of cache entries revalidated via conditional GET (304 Not Modified)",
	})

	promSummaries = make(map[string]prometheus.Summary)
	promSummaries["CACHE_READ_MEMORY"] = promauto.NewSummary(prometheus.SummaryOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_read_memory_bytes",
		Help: "The total data size of requests that were served from memory cache",
	})
	promSummaries["CACHE_READ_FILE"] = promauto.NewSummary(prometheus.SummaryOpts{
		Name: config.PrometheusMetricPrefix + "pkgproxy_cache_read_file_bytes",
		Help: "The total data size of requests that were served from the file system",
	})
}

func handleGet(w http.ResponseWriter, r *http.Request) {
	promCounters["TOTAL_REQUESTS"].Inc()

	requesterIP, err := h.GetRequestClientIp(r, config.ProxyNetworks)
	if err != nil {
		handleError(nil, err, w)
		return
	}
	olo.Info("Incoming request '%s' from '%s' on '%s'", r.URL.String(), requesterIP, r.Host)
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "PATCH" && r.Method != "DELETE" {
		olo.Warn("Incoming nonGET HTTP request '%s' from '%s' on '%s'", r.URL.Path, requesterIP, r.Host)
		errorMessage := fmt.Sprintf("HTTP method '%s' other than GET, HEAD or PATCH not allowed for '%s' from '%s' on '%s'", r.Method, r.URL, requesterIP, r.Host)
		promCounters["TOTAL_HTTP_NONGET_REQUESTS"].Inc()
		handleError(nil, errors.New(errorMessage), w)
		return
	}
	protocol := "http://"
	if r.TLS != nil {
		promCounters["TOTAL_HTTPS_REQUESTS"].Inc()
		protocol = "https://"
	} else {
		promCounters["TOTAL_HTTP_REQUESTS"].Inc()
	}

	// try to detect specific service names and set
	// those specific default cache TTL
	// this way servicename-1y.domain.tld could be used
	// to change the default cache TTL to 1 year and so on
	defaultCacheTTL := config.DefaultCacheTTL
	for name, cr := range config.ServiceNameDefaultCacheTTL {
		// olo.Debug("comparing regex rule: '%s' with regex '%s' with cacheURL: '%s'", name, cr.Regex, cacheURL)
		if cr.CompiledRegex.MatchString(r.Host) {
			olo.Debug("found matching service name regex rule: '%s' with regex '%s' and default ttl '%s' for service name: '%s'", name, cr.Regex, cr.TTL, r.Host)
			defaultCacheTTL = cr.TTL
			olo.Debug("setting default ttl to '%s' for service name '%s'", defaultCacheTTL.String(), r.Host)
			break
		}
	}

	if r.Method == "DELETE" {
		delURL := strings.TrimLeft(r.URL.String(), "/")
		if strings.Contains(delURL, "..") {
			http.Error(w, ".. not allowed in request URL", http.StatusBadRequest)
			return
		}
		switch {
		case strings.HasPrefix(delURL, "https:"):
			protocol = "https://"
			delURL = strings.TrimPrefix(delURL, "https:")
		case strings.HasPrefix(delURL, "http:"):
			protocol = "http://"
			delURL = strings.TrimPrefix(delURL, "http:")
		}
		urlPrefix := protocol + delURL
		count, err := cache.invalidateByPrefix(urlPrefix, config.CompiledMetadataPatterns)
		if err != nil {
			handleError(nil, err, w)
			return
		}
		promCounters["CACHE_PREFIX_INVALIDATE"].Inc()
		olo.Info("DELETE invalidated %d metadata cache entries for prefix '%s' from '%s'", count, urlPrefix, requesterIP)
		w.Header().Set("X-Pkgproxy-Invalidated", strconv.Itoa(count))
		fmt.Fprintf(w, "Invalidated %d metadata cache entries for prefix: %s\n", count, urlPrefix)
		return
	}

	cacheURL := strings.TrimLeft(r.URL.String(), "/")
	err = validateCacheURL(cacheURL)
	if err != nil {
		handleError(nil, err, w)
		return
	}
	// Override destination protocol based on host prefix (http:... or https:...)
	// and serve /local/<path>/<file> from local directory (local_root)
	switch {
	case strings.HasPrefix(cacheURL, "https:"):
		protocol = "https://"
		cacheURL = strings.TrimPrefix(cacheURL, "https:")
	case strings.HasPrefix(cacheURL, "http:"):
		protocol = "http://"
		cacheURL = strings.TrimPrefix(cacheURL, "http:")
	case strings.HasPrefix(cacheURL, "local/"):
		if config.LocalRoot == "" {
			olo.Error("Local fileserving not configured for %s", cacheURL)
			http.Error(w, "Local fileserving not configured", http.StatusForbidden)
			return
		}
		//localfile := config.LocalRoot + strings.TrimPrefix(cacheURL, "local")
		elements := append([]string{config.LocalRoot}, strings.Split(strings.TrimPrefix(cacheURL, "local"), "/")...)
		localfile := filepath.Join(elements...)
		olo.Info("Local fileserving: %s from %s", cacheURL, localfile)

		_, err := os.Stat(localfile)
		if err != nil {
			olo.Error("Local file '%s' not found for request to '%s'", localfile, cacheURL)
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, localfile)
		return
	}

	fullUrl := protocol + cacheURL
	olo.Info("Full incoming request for '%s' from '%s'", fullUrl, requesterIP)

	requestedURLParts := strings.Split(cacheURL, "/")
	if len(requestedURLParts) > 1 {
		requestedFQDN := requestedURLParts[0]
		requestedFQDNSave := strings.ReplaceAll(requestedFQDN, ".", "_")
		requestedFQDNSave = strings.ReplaceAll(requestedFQDNSave, "-", "_")

		if _, ok := promCounters[requestedFQDN]; !ok {
			promCounters[requestedFQDN] = promauto.NewCounter(prometheus.CounterOpts{
				Name: config.PrometheusMetricPrefix + "pkgproxy_" + requestedFQDNSave + "_total",
				Help: "The total number of requests for " + requestedFQDN,
			})
		}

		promCounters[requestedFQDN].Inc()
	}

	// PATCH always clears any negative cache entry so the next GET re-fetches upstream.
	if r.Method == "PATCH" && config.NegativeCacheTTL > 0 {
		if found, _ := cache.isNotFound(fullUrl); found {
			cache.deleteNotFoundEntry(fullUrl)
			olo.Info("NEGATIVE_CACHE_INVALIDATE via PATCH for '%s'", fullUrl)
			promCounters["NEGATIVE_CACHE_INVALIDATE"].Inc()
		}
	}

	// Fast path: URL is known to return 404, serve from negative cache (skip for PATCH).
	if r.Method != "PATCH" && config.NegativeCacheTTL > 0 {
		if found, expiry := cache.isNotFound(fullUrl); found {
			olo.Info("NEGATIVE_CACHE_HIT for '%s'", fullUrl)
			promCounters["NEGATIVE_CACHE_HIT"].Inc()
			w.Header().Set("X-Pkgproxy-Negative-Cache", "HIT")
			w.Header().Set("X-Pkgproxy-Cache-Expires", expiry.UTC().Format(time.RFC3339))
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
	}

	// Cache miss -> Load data from requested URL and add to cache
	if busy, ok := cache.has(fullUrl); !ok {
		olo.Info("CACHE_MISS for requested '%s'", fullUrl)
		promCounters["CACHE_MISS"].Inc()
		if r.Method == "PATCH" {
			olo.Info("PATCH request for non-cached item '%s' - returning 404", fullUrl)
			cache.cancelBusy(fullUrl)
			busy.Unlock()
			http.Error(w, "Cache item not found", http.StatusNotFound)
			return
		}
		// Re-check after waiting in has() — handles concurrent requests for a
		// URL that was just 404'd and stored in the negative cache by another goroutine.
		if config.NegativeCacheTTL > 0 {
			if found, expiry := cache.isNotFound(fullUrl); found {
				cache.cancelBusy(fullUrl)
				busy.Unlock()
				olo.Info("NEGATIVE_CACHE_HIT for '%s'", fullUrl)
				promCounters["NEGATIVE_CACHE_HIT"].Inc()
				w.Header().Set("X-Pkgproxy-Negative-Cache", "HIT")
				w.Header().Set("X-Pkgproxy-Cache-Expires", expiry.UTC().Format(time.RFC3339))
				http.Error(w, "File not found", http.StatusNotFound)
				return
			}
		}
		defer busy.Unlock()

		// Stream the download to the requester while it is cached, so large
		// files start flowing immediately instead of after the complete
		// download. Range and HEAD requests need the complete file first.
		var stream *streamWriter
		var onOK func(*http.Response) io.Writer
		if r.Method == "GET" && r.Header.Get("Range") == "" {
			stream = &streamWriter{w: w}
			onOK = stream.start
		}
		response, err := getRemote(fullUrl, onOK)
		if stream != nil && stream.started {
			if err != nil {
				cache.cancelBusy(fullUrl)
				// The status line and headers are already sent, so the failure
				// can only be signalled by aborting the connection. A clean
				// EOF would look like a complete download to the client.
				olo.Error("download of '%s' failed after %d bytes were streamed to '%s': %s", fullUrl, stream.written, requesterIP, err)
				panic(http.ErrAbortHandler)
			}
			olo.Info("STREAMED '%s' to '%s' while caching (%d bytes)", fullUrl, requesterIP, stream.written)
			return
		}
		if err != nil {
			cache.cancelBusy(fullUrl)
			if response != nil && response.StatusCode == http.StatusNotFound && config.NegativeCacheTTL > 0 {
				if nerr := cache.putNotFound(fullUrl); nerr != nil {
					olo.Error("putNotFound failed for %s: %s", fullUrl, nerr)
				}
				olo.Info("NEGATIVE_CACHE_PUT for '%s'", fullUrl)
				promCounters["NEGATIVE_CACHE_PUT"].Inc()
				expiry := time.Now().Add(cache.negativeCacheTTL(fullUrl))
				w.Header().Set("X-Pkgproxy-Error", "remote returned HTTP 404")
				w.Header().Set("X-Pkgproxy-Cache-Expires", expiry.UTC().Format(time.RFC3339))
				http.Error(w, "File not found", http.StatusNotFound)
			} else {
				handleError(response, err, w)
			}
			return
		}
	} else {
		olo.Info("CACHE_HIT for requested '%s'", cacheURL)
		promCounters["CACHE_HIT"].Inc()
	}

	invalidateCache := r.Method == "PATCH"

	// The cache has definitely the data we want, so get a reader for that
	cacheResponse, err := cache.get(fullUrl, defaultCacheTTL, invalidateCache)

	if err != nil {
		handleError(nil, err, w)
	} else {
		// make sure that content is only supposed to be downloaded
		// browsers will never display content
		// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition
		w.Header().Set("Content-Disposition", "attachment")
		http.ServeContent(w, r, cacheURL, cacheResponse.loadedAt, cacheResponse.content)
	}
}

// streamWriter forwards the body of an upstream download to the requester
// while it is being cached. It sends the response headers with the first
// chunk, so an upstream failure before any data arrived can still be answered
// with a proper error status. If the requester goes away, the writer stops
// forwarding but reports no error, so the download still completes and
// is cached.
type streamWriter struct {
	w          http.ResponseWriter
	started    bool
	clientGone bool
	written    int64
	contentLen int64
}

// start records the upstream response and returns the writer to tee the body to.
func (s *streamWriter) start(response *http.Response) io.Writer {
	s.contentLen = response.ContentLength
	return s
}

func (s *streamWriter) Write(p []byte) (int, error) {
	if s.clientGone {
		return len(p), nil
	}
	if !s.started {
		s.started = true
		hdr := s.w.Header()
		// make sure that content is only supposed to be downloaded
		hdr.Set("Content-Disposition", "attachment")
		hdr.Set("Accept-Ranges", "bytes")
		hdr.Set("Last-Modified", time.Now().UTC().Format(http.TimeFormat))
		if s.contentLen >= 0 {
			hdr.Set("Content-Length", strconv.FormatInt(s.contentLen, 10))
		}
	}
	// the server's WriteTimeout limits the whole response, extend it with every
	// chunk so long running downloads are not cut off while data keeps flowing
	if config.Timeout > 0 {
		http.NewResponseController(s.w).SetWriteDeadline(time.Now().Add(time.Duration(config.Timeout) * time.Second))
	}
	n, err := s.w.Write(p)
	s.written += int64(n)
	if f, ok := s.w.(http.Flusher); ok && err == nil {
		// don't let the server hold back small chunks of a slow upstream
		f.Flush()
	}
	if err != nil {
		olo.Warn("requester went away while streaming, continuing to cache: %s", err)
		s.clientGone = true
	}
	return len(p), nil
}

// tryConditionalGet attempts to revalidate the cached item for requestedURL
// using HTTP conditional GET (If-None-Match / If-Modified-Since). Returns
// (true, nil) if the cache was handled (304 kept, or 200 updated). Returns
// (false, nil) if no validators exist or an unexpected status was received, in
// which case the caller should fall back to a full GetRemote. Returns
// (false, err) on transport or caching errors.
func tryConditionalGet(requestedURL string) (bool, error) {
	cacheFile, err := cacheFilePath(requestedURL)
	if err != nil {
		return false, nil
	}
	meta := loadMeta(cacheFile)
	if meta.ETag == "" && meta.LastModified == "" {
		return false, nil
	}

	req, err := http.NewRequest("GET", requestedURL, nil)
	if err != nil {
		return false, nil
	}
	req.Header.Set("User-Agent", "https://github.com/xorpaul/pkgproxy/")
	req.Header.Set("Connection", "keep-alive")
	if meta.ETag != "" {
		req.Header.Set("If-None-Match", meta.ETag)
	}
	if meta.LastModified != "" {
		req.Header.Set("If-Modified-Since", meta.LastModified)
	}

	before := time.Now()
	response, err := doWithStallTimeout(req)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusNotModified:
		olo.Info("CACHE_REVALIDATED (304 Not Modified) for '%s' in %.5fs", requestedURL, time.Since(before).Seconds())
		promCounters["CACHE_REVALIDATED"].Inc()
		now := time.Now()
		if err := os.Chtimes(cacheFile, now, now); err != nil {
			olo.Warn("tryConditionalGet: could not touch mtime for %s: %s", cacheFile, err)
		}
		cache.refreshLoadedAt(requestedURL)
		// Some servers echo updated validators in 304; keep existing ones when absent.
		newETag := response.Header.Get("ETag")
		newLastModified := response.Header.Get("Last-Modified")
		if newETag == "" {
			newETag = meta.ETag
		}
		if newLastModified == "" {
			newLastModified = meta.LastModified
		}
		if err := saveMeta(cacheFile, CacheMeta{ETag: newETag, LastModified: newLastModified}); err != nil {
			olo.Warn("tryConditionalGet: could not save meta for %s: %s", requestedURL, err)
		}
		return true, nil

	case http.StatusOK:
		olo.Info("CACHE_CONDITIONAL_UPDATED (200) for '%s' in %.5fs", requestedURL, time.Since(before).Seconds())
		promCounters["REMOTE_OK"].Inc()
		var reader io.Reader = response.Body
		if err := cache.put(requestedURL, &reader, response.ContentLength); err != nil {
			return false, err
		}
		newMeta := CacheMeta{
			ETag:         response.Header.Get("ETag"),
			LastModified: response.Header.Get("Last-Modified"),
		}
		if newMeta.ETag != "" || newMeta.LastModified != "" {
			if err := saveMeta(cacheFile, newMeta); err != nil {
				olo.Warn("tryConditionalGet: could not save meta for %s: %s", requestedURL, err)
			}
		}
		return true, nil

	default:
		olo.Debug("tryConditionalGet: unexpected status %d for '%s', falling back to full download", response.StatusCode, requestedURL)
		return false, nil
	}
}

// upstreamStallTimeout is how long an upstream request may wait for response
// headers or for the next chunk of the body before it is aborted.
var upstreamStallTimeout = 3 * time.Minute

// doWithStallTimeout performs req like client.Do, but instead of a hard limit
// on the total request time it aborts the request if the upstream sends no
// data for upstreamStallTimeout. Downloads of any size succeed as long as data
// keeps flowing.
func doWithStallTimeout(req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancel(req.Context())
	timer := time.AfterFunc(upstreamStallTimeout, cancel)
	response, err := client.Do(req.WithContext(ctx))
	if err != nil {
		timer.Stop()
		cancel()
		return response, err
	}
	response.Body = &stallTimeoutBody{ReadCloser: response.Body, timer: timer, cancel: cancel}
	return response, nil
}

type stallTimeoutBody struct {
	io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *stallTimeoutBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.timer.Reset(upstreamStallTimeout)
	return n, err
}

func (b *stallTimeoutBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

func GetRemote(requestedURL string) (*http.Response, error) {
	return getRemote(requestedURL, nil)
}

// getRemote downloads requestedURL into the cache. If onOK is not nil it is
// called with the upstream 200 response before the body is read and returns a
// writer that receives the body while it is being cached (streaming it to the
// requester).
func getRemote(requestedURL string, onOK func(*http.Response) io.Writer) (*http.Response, error) {
	if len(config.Proxy) > 0 {
		olo.Info("GETing %s with proxy %s", requestedURL, config.Proxy)
	} else {
		olo.Info("GETing %s without proxy", requestedURL)
	}

	req, err := http.NewRequest("GET", requestedURL, nil)
	if err != nil {
		olo.Warn("Error creating GET request for %s Error: %s", requestedURL, err.Error())
	}
	req.Header.Set("User-Agent", "https://github.com/xorpaul/pkgproxy/")
	req.Header.Set("Connection", "keep-alive")

	before := time.Now()
	response, err := doWithStallTimeout(req)
	if err != nil {
		return response, err
	}

	var reader io.Reader = response.Body

	if response.StatusCode == 200 {
		promCounters["REMOTE_OK"].Inc()
		duration := time.Since(before).Seconds()
		olo.Debug("GETing %s took %.5fs", requestedURL, duration)
		var client io.Writer
		if onOK != nil {
			client = onOK(response)
		}
		err = cache.putTee(requestedURL, &reader, response.ContentLength, client)
		if err != nil {
			response.Body.Close()
			return response, err
		}
		// Persist ETag / Last-Modified so future TTL expiries can use
		// conditional GETs instead of always re-downloading the full body.
		metaVals := CacheMeta{
			ETag:         response.Header.Get("ETag"),
			LastModified: response.Header.Get("Last-Modified"),
		}
		if metaVals.ETag != "" || metaVals.LastModified != "" {
			if cacheFile, ferr := cacheFilePath(requestedURL); ferr == nil {
				if merr := saveMeta(cacheFile, metaVals); merr != nil {
					olo.Warn("GetRemote: could not save meta for %s: %s", requestedURL, merr)
				}
			}
		}
		defer response.Body.Close()
		return response, nil
	} else {
		promCounters["REMOTE_ERRORS"].Inc()
		return response, errors.New("GET " + requestedURL + " returned " + strconv.Itoa(response.StatusCode))
	}
}
