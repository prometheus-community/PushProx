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
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/alecthomas/kingpin/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promslog"
	"github.com/prometheus/common/promslog/flag"

	"github.com/prometheus-community/pushprox/util"
)

const (
	namespace = "pushprox_proxy" // For Prometheus metrics.
)

var (
	listenAddress        = kingpin.Flag("web.listen-address", "Address to listen on for proxy and client requests.").Default(":8080").String()
	maxScrapeTimeout     = kingpin.Flag("scrape.max-timeout", "Any scrape with a timeout higher than this will have to be clamped to this.").Default("5m").Duration()
	defaultScrapeTimeout = kingpin.Flag("scrape.default-timeout", "If a scrape lacks a timeout, use this value.").Default("15s").Duration()
	authorizedPollers    = kingpin.Flag("scrape.pollers-ip", "Comma separeted list of ips addresses or networks authorized to scrap via the proxy.").Default("").String()
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
		}, []string{"path"},
	)
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

const (
	OpEgals = 1
	OpMatch = 2
)

type route struct {
	path    string
	regex   *regexp.Regexp
	handler http.HandlerFunc
}

func newRoute(op int, path string, handler http.HandlerFunc) *route {
	if op == OpEgals {
		return &route{path, nil, handler}
	} else if op == OpMatch {
		return &route{"", regexp.MustCompile("^" + path + "$"), handler}

	} else {
		return nil
	}

}

type httpHandler struct {
	logger      *slog.Logger
	coordinator *Coordinator
	mux         http.Handler
	proxy       http.Handler
	pollersNet  map[*net.IPNet]int
}

func newHTTPHandler(logger *slog.Logger, coordinator *Coordinator, mux *http.ServeMux, pollers map[*net.IPNet]int) *httpHandler {
	h := &httpHandler{logger: logger, coordinator: coordinator, mux: mux, pollersNet: pollers}

	var routes = []*route{
		newRoute(OpEgals, "/push", h.handlePush),
		newRoute(OpEgals, "/poll", h.handlePoll),
		newRoute(OpMatch, "/clients(/.*)?", h.handleListClients),
		newRoute(OpEgals, "/metrics", promhttp.Handler().ServeHTTP),
	}
	hf := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		for _, route := range routes {
			var path string

			if route == nil {
				continue
			}
			if route.regex != nil {
				if strings.HasPrefix(route.path, "/clients") {
					path = "/clients"
				}
			} else if req.URL.Path == route.path {
				path = route.path
			}
			counter := httpAPICounter.MustCurryWith(prometheus.Labels{"path": path})
			handler := promhttp.InstrumentHandlerCounter(counter, route.handler)
			histogram := httpPathHistogram.MustCurryWith(prometheus.Labels{"path": path})
			route.handler = promhttp.InstrumentHandlerDuration(histogram, handler)
			// mux.Handle(route.path, handler)
			counter.WithLabelValues("200")
			if route.path == "/push" {
				counter.WithLabelValues("500")
			}
			if route.path == "/poll" {
				counter.WithLabelValues("408")
			}
			if route.regex != nil {
				if route.regex != nil {
					if route.regex.MatchString(req.URL.Path) {
						route.handler(w, req)
						return
					}
				}
			} else if req.URL.Path == route.path {
				route.handler(w, req)
				return
			}
		}
	})
	h.mux = hf
	// proxy handler
	h.proxy = promhttp.InstrumentHandlerCounter(httpProxyCounter, http.HandlerFunc(h.handleProxy))

	return h
}

// handlePush handles scrape responses from client.
func (h *httpHandler) handlePush(w http.ResponseWriter, r *http.Request) {
	buf := &bytes.Buffer{}
	io.Copy(buf, r.Body)
	scrapeResult, err := http.ReadResponse(bufio.NewReader(buf), nil)
	if err != nil {
		h.logger.Error("Error reading pushed response:", "err", err)
		http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), http.StatusInternalServerError)
		return
	}
	scrapeId := scrapeResult.Header.Get("Id")
	h.logger.Info("Got /push", "scrape_id", scrapeId)
	err = h.coordinator.ScrapeResult(scrapeResult)
	if err != nil {
		h.logger.Error("Error pushing:", "err", err, "scrape_id", scrapeId)
		http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), http.StatusInternalServerError)
	}
}

// handlePoll handles clients registering and asking for scrapes.
func (h *httpHandler) handlePoll(w http.ResponseWriter, r *http.Request) {
	fqdn, _ := io.ReadAll(r.Body)
	request, err := h.coordinator.WaitForScrapeInstruction(strings.TrimSpace(string(fqdn)))
	if err != nil {
		h.logger.Info("Error WaitForScrapeInstruction:", "err", err)
		http.Error(w, fmt.Sprintf("Error WaitForScrapeInstruction: %s", err.Error()), http.StatusRequestTimeout)
		return
	}
	//nolint:errcheck // https://github.com/prometheus-community/PushProx/issues/111
	request.WriteProxy(w) // Send full request as the body of the response.
	h.logger.Info("Responded to /poll", "url", request.URL.String(), "scrape_id", request.Header.Get("Id"))
}

// isPoller checks if caller has an IP addr in authorized nets (if any defined). It uses RemoteAddr field
// from http.Request.
// RETURNS:
//   - true and "" if no restriction is defined
//   - true and clientip  if @ip from RemoteAddr is found in allowed nets
//   - false and "" else
func (h *httpHandler) isPoller(r *http.Request) (bool, string) {
	var (
		ispoller = false
		clientip string
	)

	if len(h.pollersNet) > 0 {
		if i := strings.Index(r.RemoteAddr, ":"); i != -1 {
			clientip = r.RemoteAddr[0:i]
		}
		for key := range h.pollersNet {
			ip := net.ParseIP(clientip)
			if key.Contains(ip) {
				ispoller = true
				break
			}
		}
	} else {
		ispoller = true
	}
	return ispoller, clientip
}

// handleListClients handles requests to list available clients as a JSON array.
func (h *httpHandler) handleListClients(w http.ResponseWriter, r *http.Request) {
	var (
		targets []*targetGroup
		lknown  int
		client  string
	)

	ispoller, clientip := h.isPoller(r)
	// if not a poller we are not authorized to get all clients, restrict query to itself hostname
	if !ispoller {
		hosts, err := net.LookupAddr(clientip)
		if err != nil {
			h.logger.Error("can't reverse client address", "err", err.Error())
		}
		if len(hosts) > 0 {
			client = strings.ToLower(strings.TrimSuffix(hosts[0], "."))
		} else {
			client = "_not_found_hostname_"
		}
	} else {
		if len(r.URL.Path) > 9 {
			client = r.URL.Path[9:]
		}
	}
	known := h.coordinator.KnownClients(client)
	lknown = len(known)
	if client != "" && lknown == 0 {
		http.Error(w, "", http.StatusNotFound)
	} else {
		targets = make([]*targetGroup, 0, lknown)
		for _, k := range known {
			targets = append(targets, &targetGroup{Targets: []string{k}})
		}
		w.Header().Set("Content-Type", "application/json")
		//nolint:errcheck // https://github.com/prometheus-community/PushProx/issues/111
		json.NewEncoder(w).Encode(targets)
	}
	h.logger.Info("Responded to /clients", "client_count", lknown)
}

// handleProxy handles proxied scrapes from Prometheus.
func (h *httpHandler) handleProxy(w http.ResponseWriter, r *http.Request) {
	if ok, clientip := h.isPoller(r); !ok {
		var clientfqdn string
		hosts, err := net.LookupAddr(clientip)
		if err != nil {
			h.logger.Error("can't reverse client address", "err", err.Error())
		}
		if len(hosts) > 0 {
			// level.Info(h.logger).Log("hosts", fmt.Sprintf("%v", hosts))
			clientfqdn = strings.ToLower(strings.TrimSuffix(hosts[0], "."))
		} else {
			clientfqdn = "_not_found_hostname_"
		}
		if !h.coordinator.checkRequestChannel(clientfqdn) {
			http.Error(w, "Not an authorized poller", http.StatusForbidden)
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), util.GetScrapeTimeout(maxScrapeTimeout, defaultScrapeTimeout, r.Header))
	defer cancel()
	request := r.WithContext(ctx)
	request.RequestURI = ""

	resp, err := h.coordinator.DoScrape(ctx, request)
	if err != nil {
		h.logger.Error("Error scraping:", "err", err, "url", request.URL.String())
		http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), http.StatusInternalServerError)
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

// return list of network addresses from the httpHandlet.pollersNet map
func (h *httpHandler) pollersNetString() string {
	if len(h.pollersNet) > 0 {
		l := make([]string, 0, len(h.pollersNet))
		for netw := range h.pollersNet {
			l = append(l, netw.String())
		}
		return strings.Join(l, ",")
	} else {
		return ""
	}
}
func main() {
	promslogConfig := promslog.Config{}
	flag.AddFlags(kingpin.CommandLine, &promslogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promslog.New(&promslogConfig)
	coordinator, err := NewCoordinator(logger)
	if err != nil {
		logger.Error("Coordinator initialization failed", "err", err)
		os.Exit(1)
	}
	pollersNet := make(map[*net.IPNet]int, 10)
	if *authorizedPollers != "" {
		networks := strings.Split(*authorizedPollers, ",")
		for _, network := range networks {
			if !strings.Contains(network, "/") {
				// detect ipv6
				if strings.Contains(network, ":") {
					network = fmt.Sprintf("%s/128", network)
				} else {
					network = fmt.Sprintf("%s/32", network)
				}
			}
			if _, subnet, err := net.ParseCIDR(network); err != nil {
				logger.Error("network is invalid", "net", network, "err", err)
				os.Exit(1)
			} else {
				pollersNet[subnet] = 1
			}
		}
	}

	mux := http.NewServeMux()
	handler := newHTTPHandler(logger, coordinator, mux, pollersNet)

	logger.Info("Listening", "address", *listenAddress)
	if len(pollersNet) > 0 {
		logger.Info("Polling restricted", "allowed", handler.pollersNetString())
	}
	if err := http.ListenAndServe(*listenAddress, handler); err != nil {
		logger.Error("Listening failed", "err", err)
		os.Exit(1)
	}
}
