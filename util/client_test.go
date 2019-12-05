package util

import (
	"net/http"
	"net/url"
	"testing"
)

func TestOverrideURLIfDesired(t *testing.T) {
	// With override
	request := &http.Request{URL: &url.URL{Scheme: "http", Host: "wrong-service:9090", Path: "/v1/user/1"}}
	overrideURL := "http://localhost:8080/metrics"
	err := OverrideURLIfDesired(&overrideURL, request)
	if err != nil {
		t.Errorf("Expected no error, got one: %v", err)
	}
	if request.URL.String() != overrideURL {
		t.Errorf("Expected %s, got %s", overrideURL, request.URL.String())
	}

	// Without override
	request = &http.Request{URL: &url.URL{Scheme: "http", Host: "localhost:9090", Path: "/metrics"}}
	overrideURL = ""
	err = OverrideURLIfDesired(&overrideURL, request)
	if err != nil {
		t.Errorf("Expected no error, got one: %v", err)
	}
	nonOverride := "http://localhost:9090/metrics"
	if request.URL.String() != nonOverride {
		t.Errorf("Expected %s, got %s", nonOverride, request.URL.String())
	}
}
