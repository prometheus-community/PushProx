package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/common/log"

	"gitlab.com/robust-perception/tug_of_war/util"
)

type Coordinator struct {
	mu        sync.Mutex
	waiting   map[string]chan *http.Request
	responses map[string]chan *http.Response
}

func NewCoordinator() *Coordinator {
	return &Coordinator{
		waiting:   map[string]chan *http.Request{},
		responses: map[string]chan *http.Response{},
	}
}

var idCounter int64

// Generate a unique ID
func genId() string {
	id := atomic.AddInt64(&idCounter, 1)
	// TODO: Add MAC address.
	// TODO: Sign these to prevent spoofing.
	return fmt.Sprintf("%d-%d-%d", time.Now().Unix(), id, os.Getpid())
}

func (c *Coordinator) getRequestChannel(fqdn string) chan *http.Request {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.waiting[fqdn]
	if !ok {
		ch = make(chan *http.Request)
		c.waiting[fqdn] = ch
	}
	return ch
}

func (c *Coordinator) getResponseChannel(id string) chan *http.Response {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch, ok := c.responses[id]
	if !ok {
		ch = make(chan *http.Response)
		c.responses[id] = ch
	}
	return ch
}

// Remove a response channel. Idempotent.
func (c *Coordinator) removeResponseChannel(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.responses, id)
}

// Request a scrape.
func (c *Coordinator) DoScrape(ctx context.Context, r *http.Request) (*http.Response, error) {
	id := genId()
	log.With("scrape_id", id).With("url", r.URL.String()).Info("DoScrape")
	r.Header.Add("Id", id)
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("Matching client not found for %q: %s", r.URL.String(), ctx.Err())
	case c.getRequestChannel(r.URL.Hostname()) <- r:
	}

	respCh := c.getResponseChannel(id)
	defer c.removeResponseChannel(id)

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-respCh:
		return resp, nil
	}
}

// Client registering to accept a scrape request. Blocking.
func (c *Coordinator) WaitForScrapeInstruction(fqdn string) (*http.Request, error) {
	log.With("fqdn", fqdn).Info("WaitForScrapeInstruction")
	// TODO: What if the client times out?
	ch := c.getRequestChannel(fqdn)
	for {
		request := <-ch
		select {
		case <-request.Context().Done():
			// Request has timed out, get another one.
		default:
			return request, nil
		}
	}
}

// Client sending a scrape result in.
func (c *Coordinator) ScrapeResult(r *http.Response) {
	id := r.Header.Get("Id")
	log.With("scrape_id", id).Info("ScrapeResult")
	r.Header.Del("Id")
	c.getResponseChannel(id) <- r
	c.removeResponseChannel(id)
}

func copyHttpResponse(resp *http.Response, w http.ResponseWriter) {
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func main() {
	coordinator := NewCoordinator()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Proxy request
		if r.URL.Host != "" {
			ctx, _ := context.WithTimeout(r.Context(), util.GetScrapeTimeout(r))
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
			coordinator.ScrapeResult(scrapeResult)
			return
		}

		http.Error(w, "404: Unknown path", 404)
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
