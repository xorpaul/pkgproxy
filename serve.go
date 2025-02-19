package main

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"strconv"
	"time"

	olo "github.com/xorpaul/sigolo"
)

// serve starts the HTTPS server with the configured SSL key and certificate
func serveTLS() {
	// TLS stuff
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_RSA_WITH_AES_128_GCM_SHA256,
		},
	}

	server := &http.Server{
		Addr:         config.ListenAddress + ":" + strconv.Itoa(config.ListenSSLPort),
		TLSConfig:    tlsConfig,
		WriteTimeout: time.Duration(config.Timeout) * time.Second,
		ReadTimeout:  time.Duration(config.Timeout) * time.Second,
		IdleTimeout:  time.Duration(config.Timeout) * time.Second,
		Handler:      http.HandlerFunc(handleGet),
	}

	olo.Info(fmt.Sprintf("Listening on https://%s:%d/", config.ListenAddress, config.ListenSSLPort))
	err := server.ListenAndServeTLS(config.CertificateFile, config.PrivateKey)
	if err != nil {
		m := "Error while trying to serve HTTPS with ssl_certificate_file " + config.CertificateFile + " and ssl_private_key " + config.PrivateKey + " " + err.Error()
		olo.Fatal(m)
	}
}

// serve starts the HTTP server
func serve() {

	server := &http.Server{
		Addr:         config.ListenAddress + ":" + strconv.Itoa(config.ListenPort),
		WriteTimeout: time.Duration(config.Timeout) * time.Second,
		ReadTimeout:  time.Duration(config.Timeout) * time.Second,
		IdleTimeout:  time.Duration(config.Timeout) * time.Second,
		Handler:      http.HandlerFunc(handleGet),
	}

	olo.Info(fmt.Sprintf("Listening on http://%s:%d/", config.ListenAddress, config.ListenPort))
	err := server.ListenAndServe()
	if err != nil {
		olo.Fatal(err.Error())
	}

}
