// Copyright 2020 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/prometheus/common/promslog"
)

func prepareTest() (*httptest.Server, Coordinator) {
	// This test server acts as the proxyURL
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/poll":
			// On /poll, respond with an HTTP request serialized in the body
			var buf bytes.Buffer
			req, _ := http.NewRequest("GET", fmt.Sprintf("http://%s/", *myFqdn), nil)
			req.Header.Set("id", "test-scrape-id")
			req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "10")
			req.Write(&buf)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(buf.Bytes())
		case "/push":
			// Accept pushed scrape results, just respond OK
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	c := Coordinator{logger: promslog.NewNopLogger()}
	*proxyURL = ts.URL + "/"
	*myFqdn = "test.local" // Set fqdn to test.local for matching hostnames

	return ts, c
}

func TestDoScrape_Success(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()

	// Setup a test target server that will be scraped by doScrape
	targetServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify Authorization header if set
		auth := r.Header.Get("Authorization")
		if auth != "" && auth != "Bearer dummy-token" {
			t.Errorf("unexpected Authorization header: %s", auth)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	}))
	defer targetServer.Close()

	// Override myFqdn to match targetServer hostname
	u, err := url.Parse(targetServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	*myFqdn = u.Hostname()

	// Prepare a scrape request targeting the test target server
	req, err := http.NewRequest("GET", targetServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("id", "scrape-id-123")
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "10")

	// Set bearerToken for authorization testing
	bearerTokenMutex.Lock()
	bearerToken = "dummy-token"
	bearerTokenMutex.Unlock()

	c.doScrape(req, targetServer.Client())
}

func TestDoScrape_FailWrongFQDN(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()

	req, err := http.NewRequest("GET", "http://wronghost.local", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("id", "fail-id")
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "10")

	// This should cause handleErr due to fqdn mismatch
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

func TestDoPush_ErrorOnInvalidProxyURL(t *testing.T) {
	c := Coordinator{logger: promslog.NewNopLogger()}
	*proxyURL = "http://%41:8080" // invalid URL (percent-encoding issue)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("test")),
		Header:     http.Header{},
	}
	req, _ := http.NewRequest("GET", "http://example.com", nil)
	err := c.doPush(resp, req, http.DefaultClient)
	if err == nil {
		t.Errorf("expected error on invalid proxy URL, got nil")
	}
}

func TestDoPoll(t *testing.T) {
	ts, c := prepareTest()
	defer ts.Close()

	err := c.doPoll(ts.Client())
	if err != nil {
		t.Fatalf("doPoll failed: %v", err)
	}
}

func TestLoopWithBackoff(t *testing.T) {
	var count int
	var mu sync.Mutex
	done := make(chan struct{})
	var once sync.Once

	bo := backoffForTest(3)

	go func() {
		err := backoff.RetryNotify(func() error {
			mu.Lock()
			defer mu.Unlock()
			count++
			if count > 2 {
				// safe close
				once.Do(func() { close(done) })
				return errors.New("forced error to stop retry")
			}
			return errors.New("temporary error")
		}, bo, func(err error, d time.Duration) {
			// No-op
		})

		if err != nil {
			// safe even if already closed
			once.Do(func() { close(done) })
		}
	}()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("loop test timed out")
	}
}

func backoffForTest(maxRetries int) backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Millisecond
	b.MaxInterval = 5 * time.Millisecond
	b.MaxElapsedTime = 10 * time.Millisecond
	return backoff.WithMaxRetries(b, uint64(maxRetries))
}

func TestWatchBearerTokenFile(t *testing.T) {
	// This function is hard to test fully without fsnotify events,
	// but we can test the initial loading of the token file.

	// Create a temporary file with a token
	tmpfile := t.TempDir() + "/tokenfile"
	tokenContent := "file-token\n"
	if err := os.WriteFile(tmpfile, []byte(tokenContent), 0600); err != nil {
		t.Fatal(err)
	}

	logger := promslog.NewNopLogger()

	// Run watchBearerTokenFile in a goroutine; it will load token initially
	go func() {
		// This will block watching the directory, so we only wait shortly
		watchBearerTokenFile(tmpfile, logger)
	}()

	// Wait briefly for the token to load
	time.Sleep(100 * time.Millisecond)

	bearerTokenMutex.RLock()
	defer bearerTokenMutex.RUnlock()
	if bearerToken != strings.TrimSpace(tokenContent) {
		t.Errorf("expected bearer token %q, got %q", strings.TrimSpace(tokenContent), bearerToken)
	}
}

func TestBearerTokenHeader(t *testing.T) {
	token := "dummy-token"
	bearerTokenMutex.Lock()
	bearerToken = token
	bearerTokenMutex.Unlock()

	var receivedToken string

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedToken = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	// Ensure myFqdn matches the test server's hostname
	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	*myFqdn = u.Hostname()

	req, err := http.NewRequest("GET", ts.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Set required headers for doScrape to accept this request
	req.Header.Set("id", "token-test-id")
	req.Header.Set("X-Prometheus-Scrape-Timeout-Seconds", "10")

	c := Coordinator{logger: promslog.NewNopLogger()}
	c.doScrape(req, ts.Client())

	expected := "Bearer dummy-token"
	if receivedToken != expected {
		t.Fatalf("expected %q, got %q", expected, receivedToken)
	}
}
