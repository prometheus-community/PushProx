package main

import (
	"bufio"
	"bytes"
	"log"
	"net/http"
	"strings"
)

func loop() {
	client := &http.Client{}
	fqdn := "localhost"

	resp, err := client.Post("http://localhost:1234/poll", "", strings.NewReader(fqdn))
	if err != nil {
		log.Fatal(err)
		return
	}
	defer resp.Body.Close()
	request, _ := http.ReadRequest(bufio.NewReader(resp.Body))
	id := request.Header.Get("id") // Needed so they can be linked.
	log.Printf("Got request for %s", request.URL.String())
	request.RequestURI = ""

	scrapeResp, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
		return
	}
	log.Printf("Scraped %s", request.URL.String())
	scrapeResp.Header.Set("id", id)
	buf := &bytes.Buffer{}
	scrapeResp.Write(buf)
	log.Println(buf.Len())

	_, err = client.Post("http://localhost:1234/push", "", buf)
	if err != nil {
		log.Fatal(err)
		return
	}
	log.Printf("Pushed scrape result for %s", request.URL.String())
}

func main() {
	for {
		loop()
	}
}
