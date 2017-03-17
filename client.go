package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ShowMax/go-fqdn"
)

var (
	myFqdn   = flag.String("fqdn", fqdn.Get(), "FQDN to register with")
	proxyUrl = flag.String("proxy-url", "http://pushprox.robustperception.io:8080", "Push proxy to talk to.")
)

func doScrape(request *http.Request) {
	client := &http.Client{}

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("Failed to scrape %s: %s", request.URL.String(), err)
		log.Print(msg)
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{},
			Body:       ioutil.NopCloser(strings.NewReader(msg)),
		}
		err = doPush(resp, request)
		if err != nil {
			log.Printf("Failed to push failed scrape result for %s: %s", request.URL.String(), err)
			return
		}
		log.Printf("Pushed failed scrape result for %s", request.URL.String())
		return
	}
	log.Printf("Scraped %s", request.URL.String())

	err = doPush(scrapeResp, request)
	if err != nil {
		log.Printf("Failed to push scrape result for %s: %s", request.URL.String(), err)
		return
	}
	log.Printf("Pushed scrape result for %s", request.URL.String())
}

// Report the result of the scrape back up to the proxy.
func doPush(resp *http.Response, request *http.Request) error {
	resp.Header.Set("id", request.Header.Get("id")) // Link the request and response

	buf := &bytes.Buffer{}
	resp.Write(buf)
	client := &http.Client{}
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
		log.Printf("Error polling: %s", err)
		time.Sleep(time.Second) // Don't pound the server.
		return
	}
	defer resp.Body.Close()
	request, _ := http.ReadRequest(bufio.NewReader(resp.Body))
	log.Printf("Got request for %s", request.URL.String())
	request.RequestURI = ""

	go doScrape(request)
}

func main() {
	flag.Parse()
	log.Printf("Using FQDN of %s", *myFqdn)
	for {
		loop()
	}
}
