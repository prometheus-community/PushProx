package main

import (
	"bufio"
	"bytes"
	"log"
	"net/http"
	"strings"
	"time"
)

func doScrape(request *http.Request) {
	client := &http.Client{}
	id := request.Header.Get("id") // Needed so they can be linked.

	scrapeResp, err := client.Do(request)
	if err != nil {
		log.Printf("Failed to scrape %s: %s", request.URL.String(), err)
		return
	}
	log.Printf("Scraped %s", request.URL.String())
	scrapeResp.Header.Set("id", id)
	buf := &bytes.Buffer{}
	scrapeResp.Write(buf)
	log.Println(buf.Len())

	_, err = client.Post("http://localhost:1234/push", "", buf)
	if err != nil {
		log.Printf("Failed to push scrape result for %s: %s", request.URL.String(), err)
		return
	}
	log.Printf("Pushed scrape result for %s", request.URL.String())
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
