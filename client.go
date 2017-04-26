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
	ctx, _ := context.WithTimeout(request.Context(), util.GetScrapeTimeout(request))
	request = request.WithContext(ctx)

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("Failed to scrape %s: %s", request.URL.String(), err)
		log.Info(msg)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = doPush(resp, request, client)
		if err != nil {
			log.Infof("Failed to push failed scrape result for %s: %s", request.Header.Get("id"), err)
			return
		}
		log.Infof("Pushed failed scrape result for %s", request.Header.Get("id"))
		return
	}
	log.Infof("Scraped for %s", request.Header.Get("id"))

	err = doPush(scrapeResp, request, client)
	if err != nil {
		log.Infof("Failed to push scrape result for %s: %s", request.Header.Get("id"), err)
		return
	}
	log.Infof("Pushed scrape result for %s", request.Header.Get("id"))
}

// Report the result of the scrape back up to the proxy.
func doPush(resp *http.Response, request *http.Request, client *http.Client) error {
	resp.Header.Set("id", request.Header.Get("id")) // Link the request and response

	buf := &bytes.Buffer{}
	resp.Write(buf)
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
	log.Infof("Got request for %q for %s", request.URL.String(), request.Header.Get("id"))
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
