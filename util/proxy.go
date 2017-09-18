package util

import (
	"net/http"
	"strconv"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"
)

var (
	maxScrapeTimeout     = kingpin.Flag("scrape.max-timeout", "Any scrape with a timeout higher than this will have to be clamped to this.").Default("5m").Duration()
	defaultScrapeTimeout = kingpin.Flag("scrape.default-timeout", "If a scrape lacks a timeout, use this value.").Default("15s").Duration()
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
