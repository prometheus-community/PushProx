package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TODO: Prevent any "localhost" or rfc1918 requests to our networks

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

// Request a scrape.
func (c *Coordinator) DoScrape(ctx context.Context, r *http.Request) (*http.Response, error) {
	log.Printf("DoScrape %q", r.URL.String())
	r.Header.Add("Id", r.URL.String())
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("Matching client not found for %q: %s", r.URL.String(), ctx.Err())
	case c.getRequestChannel(r.URL.Hostname()) <- r:
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp := <-c.getResponseChannel(r.URL.String()):
		return resp, nil
	}
}

// Client registering to accept a scrape request. Blocking.
func (c *Coordinator) WaitForScrapeInstruction(fqdn string) (*http.Request, error) {
	log.Printf("WaitForScrapeInstruction %q", fqdn)
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
	log.Printf("ScrapeResult %q", id)
	r.Header.Del("Id")
	c.getResponseChannel(id) <- r
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
			ctx, _ := context.WithTimeout(r.Context(), time.Second*10)
			request := r.WithContext(ctx)
			request.RequestURI = ""

			resp, err := coordinator.DoScrape(ctx, request)
			if err != nil {
				log.Println(err)
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
			log.Printf("Responded to /poll with %q", request.URL.String())
			return
		}

		// Scrape response from client.
		if r.URL.Path == "/push" {
			buf := &bytes.Buffer{}
			io.Copy(buf, r.Body)
			scrapeResult, _ := http.ReadResponse(bufio.NewReader(buf), nil)
			log.Printf("Got /push for %q", scrapeResult.Header.Get("Id"))
			coordinator.ScrapeResult(scrapeResult)
			return
		}

		http.Error(w, "404: Unknown path", 404)
	})

	log.Fatal(http.ListenAndServe(":8080", nil))
}
