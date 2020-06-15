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
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pkg/errors"
)

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
	if err := c.doPoll(ts.Client()); err != nil {
		t.Fatal(err)
	}
}
