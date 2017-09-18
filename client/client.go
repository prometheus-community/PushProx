package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
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

func doScrape(request *http.Request, client *http.Client, logger log.Logger) {
	level.Info(logger).Log("scrap_id", request.Header.Get("id"))

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
		level.Warn(logger).Log(msg)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = doPush(resp, request, client)
		if err != nil {
			level.Warn(logger).Log("Failed to push failed scrape response:", err)
			return
		}
		level.Info(logger).Log("Pushed failed scrape response")
		return
	}
	level.Info(logger).Log("Retrieved scrape response")
	err = doPush(scrapeResp, request, client)
	if err != nil {
		level.Warn(logger).Log("Failed to push scrape response:", err)
		return
	}
	level.Info(logger).Log("Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func doPush(resp *http.Response, origRequest *http.Request, client *http.Client) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	u, _ := url.Parse(*proxyURL + "/push")

	buf := &bytes.Buffer{}
	resp.Write(buf)
	request := &http.Request{
		Method:        "POST",
		URL:           u,
		Body:          ioutil.NopCloser(buf),
		ContentLength: int64(buf.Len()),
	}
	request = request.WithContext(origRequest.Context())
	_, err := client.Do(request)
	if err != nil {
		return err
	}
	return nil
}

func loop(logger log.Logger) {
	client := &http.Client{}
	resp, err := client.Post(*proxyURL+"/poll", "", strings.NewReader(*myFqdn))
	if err != nil {
		level.Info(logger).Log("Error polling:", err)
		time.Sleep(time.Second) // Don't pound the server. TODO: Randomised exponential backoff.
		return
	}
	defer resp.Body.Close()
	request, _ := http.ReadRequest(bufio.NewReader(resp.Body))
	level.Error(logger).Log("scrape_id", request.Header.Get("id"), "url", request.URL, "Got scrape request")

	request.RequestURI = ""

	go doScrape(request, client, logger)
}

func main() {
	allowedLevel := promlog.AllowedLevel{}
	flag.AddFlags(kingpin.CommandLine, &allowedLevel)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(allowedLevel)
	if *proxyURL == "" {
		level.Error(logger).Log("-proxy-url flag must be specified.")
	}
	level.Info(logger).Log("proxy_url", *proxyURL, "Using FQDN of", *myFqdn)

	for {
		loop(logger)
	}
}
