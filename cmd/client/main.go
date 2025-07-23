// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Showmax/go-fqdn"
	"github.com/alecthomas/kingpin/v2"
	"github.com/cenkalti/backoff/v4"
	"github.com/fsnotify/fsnotify"
	"github.com/prometheus-community/pushprox/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"
)

var (
	myFqdn           = kingpin.Flag("fqdn", "FQDN to register with").Default(fqdn.Get()).String()
	proxyURL         = kingpin.Flag("proxy-url", "Push proxy to talk to.").Required().String()
	caCertFile       = kingpin.Flag("tls.cacert", "<file> CA certificate to verify peer against").String()
	tlsCert          = kingpin.Flag("tls.cert", "<cert> Client certificate file").String()
	tlsKey           = kingpin.Flag("tls.key", "<key> Private key file").String()
	metricsAddr      = kingpin.Flag("metrics-addr", "Serve Prometheus metrics at this address").Default(":9369").String()
	bearerTokenPath  = kingpin.Flag("bearer-token-path", "<path> Path to file containing bearer token to authenticate requests").String()
	retryInitialWait = kingpin.Flag("proxy.retry.initial-wait", "Amount of time to wait after proxy failure").Default("1s").Duration()
	retryMaxWait     = kingpin.Flag("proxy.retry.max-wait", "Maximum amount of time to wait between proxy poll retries").Default("5s").Duration()
)

var (
	scrapeErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_scrape_errors_total",
			Help: "Number of scrape errors",
		},
	)
	pushErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_push_errors_total",
			Help: "Number of push errors",
		},
	)
	pollErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_poll_errors_total",
			Help: "Number of poll errors",
		},
	)
	// bearerToken holds the current token string used for authentication.
	// Access must be synchronized to avoid race conditions between the watcher and HTTP handlers.
	bearerToken      string
	bearerTokenMutex sync.RWMutex
)

func init() {
	prometheus.MustRegister(pushErrorCounter, pollErrorCounter, scrapeErrorCounter)
}

func newBackOffFromFlags() backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = *retryInitialWait
	b.Multiplier = 1.5
	b.MaxInterval = *retryMaxWait
	b.MaxElapsedTime = time.Duration(0)
	return b
}

// Coordinator for scrape requests and responses
type Coordinator struct {
	logger *slog.Logger
}

func (c *Coordinator) handleErr(request *http.Request, client *http.Client, err error) {
	c.logger.Error("Coordinator error", "error", err)
	scrapeErrorCounter.Inc()
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(err.Error())),
		Header:     http.Header{},
	}
	if err = c.doPush(resp, request, client); err != nil {
		pushErrorCounter.Inc()
		c.logger.Warn("Failed to push failed scrape response:", "err", err)
		return
	}
	c.logger.Info("Pushed failed scrape response")
}

func (c *Coordinator) doScrape(request *http.Request, client *http.Client) {
	logger := c.logger.With("scrape_id", request.Header.Get("id"))
	timeout, err := util.GetHeaderTimeout(request.Header)
	if err != nil {
		c.handleErr(request, client, err)
		return
	}
	bearerTokenMutex.RLock()
	token := bearerToken
	bearerTokenMutex.RUnlock()

	if token != "" {
		request.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	request = request.WithContext(ctx)
	// We cannot handle https requests at the proxy, as we would only
	// see a CONNECT, so use a URL parameter to trigger it.
	params := request.URL.Query()
	if params.Get("_scheme") == "https" {
		request.URL.Scheme = "https"
		params.Del("_scheme")
		request.URL.RawQuery = params.Encode()
	}

	if request.URL.Hostname() != *myFqdn {
		c.handleErr(request, client, errors.New("scrape target doesn't match client fqdn"))
		return
	}

	scrapeResp, err := client.Do(request)
	if err != nil {
		c.handleErr(request, client, fmt.Errorf("failed to scrape %s: %w", request.URL.String(), err))
		return
	}
	logger.Info("Retrieved scrape response")
	if err = c.doPush(scrapeResp, request, client); err != nil {
		pushErrorCounter.Inc()
		logger.Warn("Failed to push scrape response:", "err", err)
		return
	}
	logger.Info("Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func (c *Coordinator) doPush(resp *http.Response, origRequest *http.Request, client *http.Client) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	base, err := url.Parse(*proxyURL)
	if err != nil {
		return err
	}
	u, err := url.Parse("push")
	if err != nil {
		return err
	}
	url := base.ResolveReference(u)

	buf := &bytes.Buffer{}
	//nolint:errcheck // https://github.com/prometheus-community/PushProx/issues/111
	resp.Write(buf)
	request := &http.Request{
		Method:        "POST",
		URL:           url,
		Body:          io.NopCloser(buf),
		ContentLength: int64(buf.Len()),
	}
	request = request.WithContext(origRequest.Context())
	if _, err = client.Do(request); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) doPoll(client *http.Client) error {
	base, err := url.Parse(*proxyURL)
	if err != nil {
		c.logger.Error("Error parsing url:", "err", err)
		return fmt.Errorf("error parsing url: %w", err)
	}
	u, err := url.Parse("poll")
	if err != nil {
		c.logger.Error("Error parsing url:", "err", err)
		return fmt.Errorf("error parsing url poll: %w", err)
	}
	url := base.ResolveReference(u)
	resp, err := client.Post(url.String(), "", strings.NewReader(*myFqdn))
	if err != nil {
		c.logger.Error("Error polling:", "err", err)
		return fmt.Errorf("error polling: %w", err)
	}
	defer resp.Body.Close()

	request, err := http.ReadRequest(bufio.NewReader(resp.Body))
	if err != nil {
		c.logger.Error("Error reading request:", "err", err)
		return fmt.Errorf("error reading request: %w", err)
	}
	c.logger.Info("Got scrape request", "scrape_id", request.Header.Get("id"), "url", request.URL)

	request.RequestURI = ""

	go c.doScrape(request, client)

	return nil
}

func (c *Coordinator) loop(bo backoff.BackOff, client *http.Client) {
	op := func() error {
		return c.doPoll(client)
	}

	for {
		if err := backoff.RetryNotify(op, bo, func(err error, _ time.Duration) {
			pollErrorCounter.Inc()
		}); err != nil {
			c.logger.Error("backoff returned error", "error", err)
		}
	}
}

// I need to automatically reload the bearer token if it changes
// i.e. if it a short lived kubernetes token one
func watchBearerTokenFile(path string, logger *slog.Logger) {
	loadBearerToken := func() {
		tokenBytes, err := os.ReadFile(path)
		if err != nil {
			logger.Error("Failed to read bearer token", "path", path, "err", err)
			return
		}
		bearerTokenMutex.Lock()
		bearerToken = strings.TrimSpace(string(tokenBytes)) // also trim spaces/newlines
		bearerTokenMutex.Unlock()
		logger.Info("Bearer token loaded from file", "path", path)
	}

	loadBearerToken() // initial load

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("Failed to create fsnotify watcher", "err", err)
		os.Exit(1)
	}
	defer watcher.Close()

	tokenDir := filepath.Dir(path)
	if err := watcher.Add(tokenDir); err != nil {
		logger.Error("Failed to watch token directory", "dir", tokenDir, "err", err)
		os.Exit(1)
	}

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if event.Name == path &&
				(event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create) {
				logger.Info("Bearer token file changed, reloading", "event", event)
				loadBearerToken()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Warn("fsnotify error", "err", err)
		}
	}
}

func main() {
	promslogConfig := promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, &promslogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promslog.New(&promslogConfig)
	coordinator := Coordinator{logger: logger}

	if *proxyURL == "" {
		coordinator.logger.Error("--proxy-url flag must be specified.")
		os.Exit(1)
	}
	// Make sure proxyURL ends with a single '/'
	*proxyURL = strings.TrimRight(*proxyURL, "/") + "/"
	coordinator.logger.Info("URL and FQDN info", "proxy_url", *proxyURL, "fqdn", *myFqdn)

	tlsConfig := &tls.Config{}
	if *tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			coordinator.logger.Error("Certificate or Key is invalid", "err", err)
			os.Exit(1)
		}

		// Setup HTTPS client
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if *caCertFile != "" {
		caCert, err := os.ReadFile(*caCertFile)
		if err != nil {
			coordinator.logger.Error("Not able to read cacert file", "err", err)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			coordinator.logger.Error("Failed to use cacert file as ca certificate")
			os.Exit(1)
		}

		tlsConfig.RootCAs = caCertPool
	}

	if *metricsAddr != "" {
		go func() {
			if err := http.ListenAndServe(*metricsAddr, promhttp.Handler()); err != nil {
				coordinator.logger.Warn("ListenAndServe", "err", err)
			}
		}()
	}

	// Set bearer token based on path
	if *bearerTokenPath != "" {
		go watchBearerTokenFile(*bearerTokenPath, coordinator.logger)
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
	}

	client := &http.Client{Transport: transport}

	coordinator.loop(newBackOffFromFlags(), client)
}
