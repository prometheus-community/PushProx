package util

import (
	"net/http"
	"net/url"
)

func OverrideURLIfDesired(overrideURL *string, request *http.Request) error {
	if *overrideURL != "" {
		parsedURL, err := url.Parse(*overrideURL)
		if err != nil {
			return err
		}
		request.URL = parsedURL
	}

	return nil
}
