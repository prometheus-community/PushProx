package client

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/prometheus-community/pushprox/util"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	scrapeErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_scrape_errors_total",
			Help: "Number of scrape errors",
		},
	)
	pushErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_push_errors_total",
			Help: "Number of push errors",
		},
	)
	pollErrorCounter = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "pushprox_client_poll_errors_total",
			Help: "Number of poll errors",
		},
	)
)

func init() {
	prometheus.MustRegister(pushErrorCounter, pollErrorCounter, scrapeErrorCounter)
}

func DefaultBackoff() backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = 1 * time.Second
	b.Multiplier = 1.5
	b.MaxInterval = 5 * time.Second
	b.MaxElapsedTime = time.Duration(0)
	return b
}

// Coordinator for scrape requests and responses
type Coordinator struct {
	logger   *slog.Logger
	client   *http.Client
	bo       backoff.BackOff
	fqdn     string
	proxyUrl string
}

func NewCoordinator(logger *slog.Logger, bo backoff.BackOff, client *http.Client, fqdn, proxyURL string) (*Coordinator, error) {
	if fqdn == "" {
		return nil, errors.New("fqdn must be specified")
	}
	if proxyURL == "" {
		return nil, errors.New("proxyURL must be specified")
	}
	if bo == nil {
		logger.Warn("No backoff provided, using default")
		bo = DefaultBackoff()
	}
	c := &Coordinator{
		logger:   logger,
		client:   client,
		bo:       bo,
		fqdn:     fqdn,
		proxyUrl: proxyURL,
	}
	return c, nil
}

func (c *Coordinator) Start(ctx context.Context) {
	c.loop(ctx)
}

func (c *Coordinator) handleErr(request *http.Request, err error) {
	c.logger.Error("Coordinator error", "error", err)
	scrapeErrorCounter.Inc()
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       io.NopCloser(strings.NewReader(err.Error())),
		Header:     http.Header{},
	}
	if err = c.doPush(resp, request); err != nil {
		pushErrorCounter.Inc()
		c.logger.Warn("Failed to push failed scrape response:", "err", err)
		return
	}
	c.logger.Info("Pushed failed scrape response")
}

func (c *Coordinator) doScrape(request *http.Request) {
	logger := c.logger.With("scrape_id", request.Header.Get("id"))
	timeout, err := util.GetHeaderTimeout(request.Header)
	if err != nil {
		c.handleErr(request, err)
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), timeout)
	defer cancel()
	request = request.WithContext(ctx)
	// We cannot handle https requests at the proxy, as we would only
	// see a CONNECT, so use a URL parameter to trigger it.
	params := request.URL.Query()
	if params.Get("_scheme") == "https" {
		request.URL.Scheme = "https"
		params.Del("_scheme")
		request.URL.RawQuery = params.Encode()
	}

	if request.URL.Hostname() != c.fqdn {
		c.handleErr(request, errors.New("scrape target doesn't match client fqdn"))
		return
	}

	scrapeResp, err := c.client.Do(request)
	if err != nil {
		c.handleErr(request, fmt.Errorf("failed to scrape %s: %w", request.URL.String(), err))
		return
	}
	logger.Info("Retrieved scrape response")
	if err = c.doPush(scrapeResp, request); err != nil {
		pushErrorCounter.Inc()
		logger.Warn("Failed to push scrape response:", "err", err)
		return
	}
	logger.Info("Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func (c *Coordinator) doPush(resp *http.Response, origRequest *http.Request) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	base, err := url.Parse(c.proxyUrl)
	if err != nil {
		return err
	}
	u, err := url.Parse("push")
	if err != nil {
		return err
	}
	url := base.ResolveReference(u)

	buf := &bytes.Buffer{}
	//nolint:errcheck // https://github.com/prometheus-community/PushProx/issues/111
	resp.Write(buf)
	request := &http.Request{
		Method:        "POST",
		URL:           url,
		Body:          io.NopCloser(buf),
		ContentLength: int64(buf.Len()),
	}
	request = request.WithContext(origRequest.Context())
	if _, err = c.client.Do(request); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) doPoll(ctx context.Context) error {
	base, err := url.Parse(c.proxyUrl)
	if err != nil {
		c.logger.Error("Error parsing url:", "err", err)
		return fmt.Errorf("error parsing url: %w", err)
	}
	u, err := url.Parse("poll")
	if err != nil {
		c.logger.Error("Error parsing url:", "err", err)
		return fmt.Errorf("error parsing url poll: %w", err)
	}
	pollUrl := base.ResolveReference(u)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pollUrl.String(), strings.NewReader(c.fqdn))
	if err != nil {
		c.logger.Error("Error creating request:", "err", err)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		c.logger.Error("Error polling:", "err", err)
		return fmt.Errorf("error polling: %w", err)
	}
	defer resp.Body.Close()

	request, err := http.ReadRequest(bufio.NewReader(resp.Body))
	if err != nil {
		c.logger.Error("Error reading request:", "err", err)
		return fmt.Errorf("error reading request: %w", err)
	}
	c.logger.Info("Got scrape request", "scrape_id", request.Header.Get("id"), "url", request.URL)

	request.RequestURI = ""

	go c.doScrape(request)

	return nil
}

func (c *Coordinator) loop(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := func() error {
		return c.doPoll(ctx)
	}

	for ctx.Err() == nil {
		if err := backoff.RetryNotify(op, c.bo, func(err error, _ time.Duration) {
			pollErrorCounter.Inc()
		}); err != nil {
			c.logger.Error("backoff returned error", "error", err)
		}
	}
}
