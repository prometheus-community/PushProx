package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/ShowMax/go-fqdn"
	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
	"github.com/robustperception/pushprox/util"
)

var (
	myFqdn   = kingpin.Flag("fqdn", "FQDN to register with").Default(fqdn.Get()).String()
	proxyURL = kingpin.Flag("proxy-url", "Push proxy to talk to.").Required().String()
)

type Coordinator struct {
	logger log.Logger
}

func (c *Coordinator) doScrape(request *http.Request, client *http.Client) {
	logger := log.With(c.logger, "scrape_id", request.Header.Get("id"))
	ctx, _ := context.WithTimeout(request.Context(), util.GetScrapeTimeout(request.Header))
	request = request.WithContext(ctx)
	// We cannot handle http requests at the proxy, as we would only
	// see a CONNECT, so use a URL parameter to trigger it.
	params := request.URL.Query()
	if params.Get("_scheme") == "https" {
		request.URL.Scheme = "https"
		params.Del("_scheme")
		request.URL.RawQuery = params.Encode()
	}

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("Failed to scrape %s: %s", request.URL.String(), err)
		level.Warn(logger).Log("msg", "Failed to scrape", "Request URL", request.URL.String(), "err", err)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = c.doPush(resp, request, client)
		if err != nil {
			level.Warn(logger).Log("msg", "Failed to push failed scrape response:", "err", err)
			return
		}
		level.Info(logger).Log("msg", "Pushed failed scrape response")
		return
	}
	level.Info(logger).Log("msg", "Retrieved scrape response")
	err = c.doPush(scrapeResp, request, client)
	if err != nil {
		level.Warn(logger).Log("msg", "Failed to push scrape response:", "err", err)
		return
	}
	level.Info(logger).Log("msg", "Pushed scrape result")
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
	u, err := url.Parse("/push")
	if err != nil {
		return err
	}
	url := base.ResolveReference(u)

	buf := &bytes.Buffer{}
	resp.Write(buf)
	request := &http.Request{
		Method:        "POST",
		URL:           url,
		Body:          ioutil.NopCloser(buf),
		ContentLength: int64(buf.Len()),
	}
	request = request.WithContext(origRequest.Context())
	_, err = client.Do(request)
	if err != nil {
		return err
	}
	return nil
}

func loop(c Coordinator) {
	client := &http.Client{}
	base, err := url.Parse(*proxyURL)
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return
	}
	u, err := url.Parse("/poll")
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return
	}
	url := base.ResolveReference(u)
	resp, err := client.Post(url.String(), "", strings.NewReader(*myFqdn))
	if err != nil {
		level.Error(c.logger).Log("msg", "Error polling:", "err", err)
		time.Sleep(time.Second) // Don't pound the server. TODO: Randomised exponential backoff.
		return
	}
	defer resp.Body.Close()
	request, _ := http.ReadRequest(bufio.NewReader(resp.Body))
	level.Info(c.logger).Log("msg", "Got scrape request", "scrape_id", request.Header.Get("id"), "url", request.URL)

	request.RequestURI = ""

	go c.doScrape(request, client)
}

func main() {
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)
	coordinator := Coordinator{logger: logger}
	if *proxyURL == "" {
		level.Error(coordinator.logger).Log("msg", "-proxy-url flag must be specified.")
		os.Exit(1)
	}
	level.Info(coordinator.logger).Log("msg", "URL and FQDN info", "proxy_url", *proxyURL, "Using FQDN of", *myFqdn)
	for {
		loop(coordinator)
	}
}
