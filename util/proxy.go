package util

import (
	"flag"
	"net/http"
	"strconv"
	"time"
)

var (
	maxScrapeTimeout     = flag.Duration("scrape.max-timeout", 5*time.Minute, "Any scrape with a timeout higher than this will have to clamped to this.")
	defaultScrapeTimeout = flag.Duration("scrape.default-timeout", 15*time.Second, "If a scrape lacks a timeout, use this value.")
)

func GetScrapeTimeout(h http.Header) time.Duration {
	timeout := *defaultScrapeTimeout
	timeoutSeconds, err := strconv.ParseFloat(h.Get("X-Prometheus-Scrape-Timeout-Seconds"), 64)
	if err == nil {
		timeout = time.Duration(timeoutSeconds * 1e9)
	}
	if timeout > *maxScrapeTimeout {
		timeout = *maxScrapeTimeout
	}
	return timeout
}
