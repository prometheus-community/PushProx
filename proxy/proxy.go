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
	"time"

	"github.com/prometheus/common/log"

	"gitlab.com/robust-perception/tug_of_war/util"
)

func copyHttpResponse(resp *http.Response, w http.ResponseWriter) {
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
	coordinator := NewCoordinator()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Proxy request
		if r.URL.Host != "" {
			ctx, _ := context.WithTimeout(r.Context(), util.GetScrapeTimeout(r.Header))
			request := r.WithContext(ctx)
			request.RequestURI = ""

			resp, err := coordinator.DoScrape(ctx, request)
			if err != nil {
				log.With("url", request.URL.String()).Infof("Error scraping: %s", err)
				http.Error(w, fmt.Sprintf("Error scraping %q: %s", request.URL.String(), err.Error()), 500)
				return
			}
			defer resp.Body.Close()
			copyHttpResponse(resp, w)
			return
		}

		// Client registering and asking for scrapes.
		if r.URL.Path == "/poll" {
			fqdn, _ := ioutil.ReadAll(r.Body)
			request, _ := coordinator.WaitForScrapeInstruction(strings.TrimSpace(string(fqdn)))
			request.WriteProxy(w) // Send full request as the body of the response.
			log.With("url", request.URL.String()).With("scrape_id", request.Header.Get("Id")).Info("Responded to /poll")
			return
		}

		// Scrape response from client.
		if r.URL.Path == "/push" {
			buf := &bytes.Buffer{}
			io.Copy(buf, r.Body)
			scrapeResult, _ := http.ReadResponse(bufio.NewReader(buf), nil)
			log.With("scrape_id", scrapeResult.Header.Get("Id")).Info("Got /push")
			err := coordinator.ScrapeResult(scrapeResult)
			if err != nil {
				log.With("scrape_id", scrapeResult.Header.Get("Id")).Infof("Error pushing: %s", err)
				http.Error(w, fmt.Sprintf("Error pushing: %s", err.Error()), 500)
			}
			return
		}

		if r.URL.Path == "/clients" {
			known := coordinator.KnownClients(time.Now().Add(-5 * time.Minute))
			targets := make([]*targetGroup, 0, len(known))
			for _, k := range known {
				targets = append(targets, &targetGroup{Targets: []string{k}})
			}
			json.NewEncoder(w).Encode(targets)
			log.With("client_count", len(known)).Info("Responded to /clients")
			return
		}

		http.Error(w, "404: Unknown path", 404)
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
