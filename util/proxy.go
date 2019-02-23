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
