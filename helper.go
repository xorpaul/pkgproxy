package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	olo "github.com/xorpaul/sigolo"
)

func handleError(response *http.Response, err error, w http.ResponseWriter) {
	olo.Error("%s", err.Error())
	if response != nil {
		w.Header().Set("X-Pkgproxy-Error", fmt.Sprintf("remote returned HTTP %d", response.StatusCode))
		w.WriteHeader(response.StatusCode)
		bodyBytes, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			olo.Error("Error while reading failed response body")
		}
		w.Write(bodyBytes)
	} else {
		w.Header().Set("X-Pkgproxy-Error", "remote-unreachable")
		w.WriteHeader(500)
		fmt.Fprintf(w, "pkgproxy: could not reach the requested remote URL\n\nError: %s\n\nPlease check that the URL path is correct and the remote host is reachable from pkgproxy.\n", err.Error())
	}
}

func removeSchemeFromURL(requestedURL string) (string, error) {
	url, err := url.Parse(requestedURL)
	if err != nil {
		return "", fmt.Errorf("unable to remove URL scheme from requested URL '%s'", requestedURL)
	}
	return strings.Replace(requestedURL, url.Scheme+"://", "", 1), nil
}

func validateCacheURL(cacheURL string) error {
	if strings.Contains(cacheURL, "..") {
		return errors.New(".. is not allowed in request URL")
	}
	if strings.HasSuffix(cacheURL, "/") {
		return errors.New("request URL ending with / is not allowed")
	}
	return nil
}
