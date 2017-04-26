package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"net/http"
	"strings"
	"time"

	"github.com/ShowMax/go-fqdn"
	"github.com/prometheus/common/log"

	"gitlab.com/robust-perception/tug_of_war/util"
)

var (
	myFqdn   = flag.String("fqdn", fqdn.Get(), "FQDN to register with")
	proxyUrl = flag.String("proxy-url", "http://pushprox.robustperception.io:8080", "Push proxy to talk to.")
)

func doScrape(request *http.Request, client *http.Client) {
	logger := log.With("scrape_id", request.Header.Get("id"))
	ctx, _ := context.WithTimeout(request.Context(), util.GetScrapeTimeout(request))
	request = request.WithContext(ctx)

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("Failed to scrape %s: %s", request.URL.String(), err)
		log.Warn(msg)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = doPush(resp, request, client)
		if err != nil {
			log.Warnf("Failed to push failed scrape result: %s", err)
			return
		}
		log.Info("Pushed failed scrape result")
		return
	}
	logger.Info("Got scrape response")

	err = doPush(scrapeResp, request, client)
	if err != nil {
		logger.Warn("Failed to push scrape result: %s", err)
		return
	}
	logger.Info("Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func doPush(resp *http.Response, request *http.Request, client *http.Client) error {
	resp.Header.Set("id", request.Header.Get("id")) // Link the request and response

	buf := &bytes.Buffer{}
	resp.Write(buf)
	// TODO: Timeout
	_, err := client.Post(*proxyUrl+"/push", "", buf)
	if err != nil {
		return err
	}
	return nil
}

func loop() {
	client := &http.Client{}
	resp, err := client.Post(*proxyUrl+"/poll", "", strings.NewReader(*myFqdn))
	if err != nil {
		log.Infof("Error polling: %s", err)
		time.Sleep(time.Second) // Don't pound the server.
		return
	}
	defer resp.Body.Close()
	request, _ := http.ReadRequest(bufio.NewReader(resp.Body))
	log.With("scrape_id", request.Header.Get("id")).With("url", request.URL).Info("Got scrape request")
	request.RequestURI = ""

	go doScrape(request, client)
}

func main() {
	flag.Parse()
	log.Infof("Using FQDN of %s", *myFqdn)
	for {
		loop()
	}
}
