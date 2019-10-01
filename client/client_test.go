package main

import (
	"net/http"
	"testing"
)

func TestJitter(t *testing.T) {
	jitter := newJitter()
	jitter.calc()
	if !(jitter.min <= jitter.duration || jitter.duration <= jitter.cap) {
		t.Fatal("invalid jitter value: ", jitter.duration)
	}
}

func TestStrictMode(t *testing.T) {
	*myFqdn = "my.fqdn"
	request, err := http.NewRequest("POST", "not.my.fqdn", nil)
	if err != nil {
		t.Fatal(err)
	}
	enforceStrict(request)
	if request.Method != "GET" {
		t.Fatal("stric mode not enforcing HTTP verb")
	}
	if request.URL.Hostname() != *myFqdn {
		t.Fatal("stric mode not enforcing FQDN")
	}
}
