package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg/errors"
)

func TestJitter(t *testing.T) {
	jitter := newJitter()
	for i := 0; i < 100000; i++ {
		duration := jitter.calc()
		if !(jitter.min <= duration || duration <= jitter.cap) {
			t.Fatal("invalid jitter value: ", duration)
		}
	}
}

type TestLogger struct{}

func (tl *TestLogger) Log(vars ...interface{}) error {
	fmt.Printf("%+v\n", vars)
	return nil
}

func prepareTest() (*httptest.Server, Coordinator) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "GET /index.html HTTP/1.0\n\nOK")
	}))
	c := Coordinator{logger: &TestLogger{}}
	*proxyURL = ts.URL
	return ts, c
}

func TestDoScrape(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()

	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Add("X-Prometheus-Scrape-Timeout-Seconds", "10.0")
	*myFqdn = ts.URL
	c.doScrape(req, ts.Client())
}

func TestHandleErr(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()

	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.handleErr(req, ts.Client(), errors.New("test error"))
}

func TestLoop(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()
	if err := loop(c, ts.Client()); err != nil {
		t.Fatal(err)
	}
}
