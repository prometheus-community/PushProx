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

package util

import (
	"net/http"
	"strconv"
	"time"
)

func GetScrapeTimeout(maxScrapeTimeout, defaultScrapeTimeout *time.Duration, h http.Header) time.Duration {
	timeout := *defaultScrapeTimeout
	headerTimeout, err := GetHeaderTimeout(h)
	if err == nil {
		timeout = headerTimeout
	}
	if timeout > *maxScrapeTimeout {
		timeout = *maxScrapeTimeout
	}
	return timeout
}

func GetHeaderTimeout(h http.Header) (time.Duration, error) {
	timeoutSeconds, err := strconv.ParseFloat(h.Get("X-Prometheus-Scrape-Timeout-Seconds"), 64)
	if err != nil {
		return time.Duration(0 * time.Second), err
	}

	return time.Duration(timeoutSeconds * 1e9), nil
}
