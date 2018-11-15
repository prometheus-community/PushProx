package main

import (
	"bufio"
	"bytes"
	"context"
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
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"

	"github.com/robustperception/pushprox/util"
)

var (
	listenAddress = kingpin.Flag("web.listen-address", "Address to listen on for proxy and client requests.").Default(":8080").String()
)

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

func newHTTPHandler(logger log.Logger, coordinator *Coordinator, mux *http.ServeMux) *httpHandler {
	h := &httpHandler{logger: logger, coordinator: coordinator, mux: mux}
	mux.Handle("/clients", http.HandlerFunc(h.handleListClients))
	mux.Handle("/push", http.HandlerFunc(h.handlePush))
	mux.Handle("/poll", http.HandlerFunc(h.handlePoll))
	h.proxy = http.HandlerFunc(h.handleProxy)
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
	ctx, _ := context.WithTimeout(r.Context(), util.GetScrapeTimeout(r.Header))
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
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)
	coordinator := NewCoordinator(logger)

	handler := newHTTPHandler(logger, coordinator, http.NewServeMux())

	level.Info(logger).Log("msg", "Listening", "address", *listenAddress)
	if err := http.ListenAndServe(*listenAddress, handler); err != nil {
		level.Error(logger).Log("msg", "Listening failed", "err", err)
		os.Exit(1)
	}
}
