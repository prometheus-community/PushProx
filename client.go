package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"
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
			Body:       ioutil.NopCloser(bytes.NewBufferString(msg)),
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

func doPush(resp *http.Response, request *http.Request) error {
	resp.Header.Set("id", request.Header.Get("id")) // Link the request and response

	buf := &bytes.Buffer{}
	resp.Write(buf)
	client := &http.Client{}
	_, err := client.Post("http://localhost:1234/push", "", buf)
	if err != nil {
		return err
	}
	return nil
}

func loop() {
	client := &http.Client{}
	fqdn := "localhost"

	resp, err := client.Post("http://localhost:1234/poll", "", strings.NewReader(fqdn))
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
	for {
		loop()
	}
}
