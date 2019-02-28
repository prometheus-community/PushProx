package util

import (
	"net/http"
	"testing"
	"time"
)

func TestGetScrapeTimeout(t *testing.T) {
	// With header set
	maxScrapeTimeout := time.Duration(5 * time.Minute)
	defaultScrapeTimeout := time.Duration(10 * time.Second)
	header := http.Header{"X-Prometheus-Scrape-Timeout-Seconds": []string{"5.0"}}
	timeout := GetScrapeTimeout(&maxScrapeTimeout, &defaultScrapeTimeout, header)
	if timeout != time.Duration(5*time.Second) {
		t.Errorf("Expected 5s, got %s", timeout)
	}

	// With header unset
	header = http.Header{}
	timeout = GetScrapeTimeout(&maxScrapeTimeout, &defaultScrapeTimeout, header)
	if timeout != time.Duration(10*time.Second) {
		t.Errorf("Expected 10s, got %s", timeout)
	}

	// With header set empty
	header = http.Header{"X-Prometheus-Scrape-Timeout-Seconds": []string{}}
	timeout = GetScrapeTimeout(&maxScrapeTimeout, &defaultScrapeTimeout, header)
	if timeout != time.Duration(10*time.Second) {
		t.Errorf("Expected 10s, got %s", timeout)
	}

	// With header set higher than maxScrapeTimeout
	header = http.Header{"X-Prometheus-Scrape-Timeout-Seconds": []string{"600.0"}}
	timeout = GetScrapeTimeout(&maxScrapeTimeout, &defaultScrapeTimeout, header)
	if timeout != time.Duration(5*time.Minute) {
		t.Errorf("Expected 5m0s, got %s", timeout)
	}

	// With header set higher than defaultScrapeTimeout, lower than maxScrapeTimeout
	header = http.Header{"X-Prometheus-Scrape-Timeout-Seconds": []string{"30.0"}}
	defaultScrapeTimeout = time.Duration(10 * time.Second)
	timeout = GetScrapeTimeout(&maxScrapeTimeout, &defaultScrapeTimeout, header)
	if timeout != time.Duration(30*time.Second) {
		t.Errorf("Expected 30s, got %s", timeout)
	}
}

func TestGetHeaderTimeout(t *testing.T) {
	// With header set
	header := http.Header{"X-Prometheus-Scrape-Timeout-Seconds": []string{"5.0"}}
	timeout, err := GetHeaderTimeout(header)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
	if timeout != time.Duration(5*time.Second) {
		t.Errorf("Expected 5s, got %s", timeout)
	}

	// With header unset
	header = http.Header{}
	timeout, err = GetHeaderTimeout(header)
	if err == nil {
		t.Error("Expected error, got none")
	}
	if timeout != time.Duration(0*time.Second) {
		t.Errorf("Expected 0s, got %s", timeout)
	}

}
