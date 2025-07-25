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
	"regexp"
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

	client = &http.Client{
		Timeout:   time.Minute * 3,
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
	if r.Method != "GET" && r.Method != "HEAD" && r.Method != "PATCH" {
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
		re := regexp.MustCompile(cr.Regex)
		// olo.Debug("comparing regex rule: '%s' with regex '%s' with cacheURL: '%s'", name, cr.Regex, cacheURL)
		if re.MatchString(r.Host) {
			olo.Debug("found matching service name regex rule: '%s' with regex '%s' and default ttl '%s' for service name: '%s'", name, cr.Regex, cr.TTL, r.Host)
			defaultCacheTTL = cr.TTL
			olo.Debug("setting default ttl to '%s' for service name '%s'", defaultCacheTTL.String(), r.Host)
			break
		}
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

	// Cache miss -> Load data from requested URL and add to cache
	if busy, ok := cache.has(fullUrl); !ok {
		olo.Info("CACHE_MISS for requested '%s'", fullUrl)
		promCounters["CACHE_MISS"].Inc()
		defer busy.Unlock()
		response, err := GetRemote(fullUrl)
		if err != nil {
			handleError(response, err, w)
			return
		}
	} else {
		olo.Info("CACHE_HIT for requested '%s'", cacheURL)
		promCounters["CACHE_HIT"].Inc()
	}

	invalidateCache := false
	if r.Method == "PATCH" {
		invalidateCache = true
	}

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

func GetRemote(requestedURL string) (*http.Response, error) {
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
	response, err := client.Do(req)
	if err != nil {
		return response, err
	}

	var reader io.Reader = response.Body

	if response.StatusCode == 200 {
		promCounters["REMOTE_OK"].Inc()
		duration := time.Since(before).Seconds()
		olo.Debug("GETing %s took %.5fs", requestedURL, duration)
		err = cache.put(requestedURL, &reader, response.ContentLength)
		if err != nil {
			return response, err
		}
		defer response.Body.Close()
		return response, nil
	} else {
		promCounters["REMOTE_ERRORS"].Inc()
		return response, errors.New("GET " + requestedURL + " returned " + strconv.Itoa(response.StatusCode))
	}
}
