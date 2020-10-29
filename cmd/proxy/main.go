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
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"

	"github.com/prometheus-community/pushprox/util"
)

const (
	namespace = "pushprox_proxy" // For Prometheus metrics.
)

var (
	authUser             = kingpin.Flag("web.auth.username", "Basic auth username").Default("").String()
	authPassword         = kingpin.Flag("web.auth.password", "Basic auth password").Default("").String()
	disableClients       = kingpin.Flag("web.disable-clients", "Disable /clients endpoint").Default("false").Bool()
	listenAddress        = kingpin.Flag("web.listen-address", "Address to listen on for proxy and client requests.").Default(":8080").String()
	maxScrapeTimeout     = kingpin.Flag("scrape.max-timeout", "Any scrape with a timeout higher than this will have to be clamped to this.").Default("5m").Duration()
	defaultScrapeTimeout = kingpin.Flag("scrape.default-timeout", "If a scrape lacks a timeout, use this value.").Default("15s").Duration()
)

var (
	httpAPICounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushprox_http_requests_total",
			Help: "Number of http api requests.",
		}, []string{"code", "path"},
	)

	httpProxyCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushproxy_proxied_requests_total",
			Help: "Number of http proxy requests.",
		}, []string{"code"},
	)
	httpPathHistogram = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "pushprox_http_duration_seconds",
			Help: "Time taken by path",
		}, []string{"path"})
)

func init() {
	prometheus.MustRegister(httpAPICounter, httpProxyCounter, httpPathHistogram)
}

func copyHTTPResponse(resp *http.Response, w http.ResponseWriter) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

type targetGroup struct {
	Targets []string          `json:"targets"`
	Labels  map[string]string `json:"labels"`
}

type httpHandler struct {
	logger      log.Logger
	coordinator *Coordinator
	mux         http.Handler
	proxy       http.Handler
}

func basicAuth(handler http.HandlerFunc) http.HandlerFunc {
	if *authUser == "" && *authPassword == "" {
		return handler
	}
	return func(w http.ResponseWriter, r *http.Request) {

		user, pass, ok := r.BasicAuth()

		if !ok || subtle.ConstantTimeCompare([]byte(user), []byte(*authUser)) != 1 || subtle.ConstantTimeCompare([]byte(pass), []byte(*authPassword)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Authentication required"`)
			w.WriteHeader(401)
			w.Write([]byte("Unauthorised.\n"))
			return
		}

		handler(w, r)
	}
}

func newHTTPHandler(logger log.Logger, coordinator *Coordinator, mux *http.ServeMux) *httpHandler {
	h := &httpHandler{logger: logger, coordinator: coordinator, mux: mux}

	// api handlers
	handlers := map[string]http.HandlerFunc{
		"/push":    h.handlePush,
		"/poll":    h.handlePoll,
		"/metrics": promhttp.Handler().ServeHTTP,
	}

	if !*disableClients {
		handlers["/clients"] = basicAuth(h.handleListClients)
	}

	for path, handlerFunc := range handlers {
		counter := httpAPICounter.MustCurryWith(prometheus.Labels{"path": path})
		handler := promhttp.InstrumentHandlerCounter(counter, http.HandlerFunc(handlerFunc))
		histogram := httpPathHistogram.MustCurryWith(prometheus.Labels{"path": path})
		handler = promhttp.InstrumentHandlerDuration(histogram, handler)
		mux.Handle(path, handler)
		counter.WithLabelValues("200")
		if path == "/push" {
			counter.WithLabelValues("500")
		}
		if path == "/poll" {
			counter.WithLabelValues("408")
		}
	}

	// proxy handler
	h.proxy = promhttp.InstrumentHandlerCounter(httpProxyCounter, http.HandlerFunc(basicAuth(h.handleProxy)))

	return h
}

// handlePush handles scrape responses from client.
func (h *httpHandler) handlePush(w http.ResponseWriter, r *http.Request) {
	buf := &bytes.Buffer{}
	io.Copy(buf, r.Body)
	scrapeResult, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		level.Error(h.logger).Log("msg", "Error reading pushed response:", "err", err)
		http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), 500)
		return
	}
	level.Info(h.logger).Log("msg", "Got /push", "scrape_id", scrapeResult.Header.Get("Id"))
	err = h.coordinator.ScrapeResult(scrapeResult)
	if err != nil {
		level.Error(h.logger).Log("msg", "Error pushing:", "err", err, "scrape_id", scrapeResult.Header.Get("Id"))
		http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), 500)
	}
}

// handlePoll handles clients registering and asking for scrapes.
func (h *httpHandler) handlePoll(w http.ResponseWriter, r *http.Request) {
	fqdn, _ := ioutil.ReadAll(r.Body)
	request, err := h.coordinator.WaitForScrapeInstruction(strings.TrimSpace(string(fqdn)))
	if err != nil {
		level.Info(h.logger).Log("msg", "Error WaitForScrapeInstruction:", "err", err)
		http.Error(w, fmt.Sprintf("Error WaitForScrapeInstruction: %s", err.Error()), 408)
		return
	}
	request.WriteProxy(w) // Send full request as the body of the response.
	level.Info(h.logger).Log("msg", "Responded to /poll", "url", request.URL.String(), "scrape_id", request.Header.Get("Id"))
}

// handleListClients handles requests to list available clients as a JSON array.
func (h *httpHandler) handleListClients(w http.ResponseWriter, r *http.Request) {
	known := h.coordinator.KnownClients()
	targets := make([]*targetGroup, 0, len(known))
	for _, k := range known {
		targets = append(targets, &targetGroup{Targets: []string{k}})
	}
	json.NewEncoder(w).Encode(targets)
	level.Info(h.logger).Log("msg", "Responded to /clients", "client_count", len(known))
}

// handleProxy handles proxied scrapes from Prometheus.
func (h *httpHandler) handleProxy(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), util.GetScrapeTimeout(maxScrapeTimeout, defaultScrapeTimeout, r.Header))
	defer cancel()
	request := r.WithContext(ctx)
	request.RequestURI = ""

	resp, err := h.coordinator.DoScrape(ctx, request)
	if err != nil {
		level.Error(h.logger).Log("msg", "Error scraping:", "err", err, "url", request.URL.String())
		http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), 500)
		return
	}
	defer resp.Body.Close()
	copyHTTPResponse(resp, w)
}

// ServeHTTP discriminates between proxy requests (e.g. from Prometheus) and other requests (e.g. from the Client).
func (h *httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Host != "" { // Proxy request
		h.proxy.ServeHTTP(w, r)
	} else { // Non-proxy requests
		h.mux.ServeHTTP(w, r)
	}
}

func main() {
	promlogConfig := promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, &promlogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(&promlogConfig)
	coordinator, err := NewCoordinator(logger)
	if err != nil {
		level.Error(logger).Log("msg", "Coordinator initialization failed", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	handler := newHTTPHandler(logger, coordinator, mux)

	level.Info(logger).Log("msg", "Listening", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, handler); err != nil {
		level.Error(logger).Log("msg", "Listening failed", "err", err)
		os.Exit(1)
	}
}
