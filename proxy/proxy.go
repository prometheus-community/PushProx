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
	"strings"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"

	"github.com/robustperception/PushProx/util"
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

func main() {
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)
	coordinator := NewCoordinator(logger)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Proxy request
		if r.URL.Host != "" {
			ctx, _ := context.WithTimeout(r.Context(), util.GetScrapeTimeout(r.Header))
			request := r.WithContext(ctx)
			request.RequestURI = ""

			resp, err := coordinator.DoScrape(ctx, request, logger)
			if err != nil {
				level.Error(logger).Log("url", "Error scraping: %s", err)
				http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), 500)
				return
			}
			defer resp.Body.Close()
			copyHTTPResponse(resp, w)
			return
		}

		// Client registering and asking for scrapes.
		if r.URL.Path == "/poll" {
			fqdn, _ := ioutil.ReadAll(r.Body)
			request, _ := coordinator.WaitForScrapeInstruction(strings.TrimSpace(string(fqdn)), logger)
			request.WriteProxy(w) // Send full request as the body of the response.
			level.Info(logger).Log("url", request.URL.String(), "scrape_id", request.Header.Get("Id"), "Responded to /poll")
			return
		}

		// Scrape response from client.
		if r.URL.Path == "/push" {
			buf := &bytes.Buffer{}
			io.Copy(buf, r.Body)
			scrapeResult, _ := http.ReadResponse(bufio.NewReader(buf), nil)
			level.Info(logger).Log("scrape_id", scrapeResult.Header.Get("Id"), "Got /push")
			err := coordinator.ScrapeResult(scrapeResult, logger)
			if err != nil {
				level.Error(logger).Log("scrape_id", scrapeResult.Header.Get("Id"), "Error pushing: %s", err)
				http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), 500)
			}
			return
		}

		if r.URL.Path == "/clients" {
			known := coordinator.KnownClients()
			targets := make([]*targetGroup, 0, len(known))
			for _, k := range known {
				targets = append(targets, &targetGroup{Targets: []string{k}})
			}
			json.NewEncoder(w).Encode(targets)
			level.Error(logger).Log("client_count", len(known), "Responded to /clients")
			return
		}

		http.Error(w, "404: Unknown path", 404)
	})

	level.Info(logger).Log("address", *listenAddress, "Listening")
	log.Fatal(http.ListenAndServe(*listenAddress, nil))
}
