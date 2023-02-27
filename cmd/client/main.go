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
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/Showmax/go-fqdn"
	"github.com/cenkalti/backoff/v4"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	kingpin "gopkg.in/alecthomas/kingpin.v2"

	"github.com/go-kit/log"
	"github.com/go-kit/log/level"
	"github.com/pkg/errors"
	"github.com/prometheus-community/pushprox/util"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/promlog"
	"github.com/prometheus/common/promlog/flag"
)

var (
	myFqdn      = kingpin.Flag("fqdn", "FQDN to register with").Default(fqdn.Get()).String()
	fqdnList    = kingpin.Flag("fqdnList", "Specify comma separated FQDN values, if running for more than one FQDN").String()
	proxyURL    = kingpin.Flag("proxy-url", "Push proxy to talk to.").Required().String()
	caCertFile  = kingpin.Flag("tls.cacert", "<file> CA certificate to verify peer against").String()
	tlsCert     = kingpin.Flag("tls.cert", "<cert> Client certificate file").String()
	tlsKey      = kingpin.Flag("tls.key", "<key> Private key file").String()
	metricsAddr = kingpin.Flag("metrics-addr", "Serve Prometheus metrics at this address").Default(":9369").String()

	retryInitialWait = kingpin.Flag("proxy.retry.initial-wait", "Amount of time to wait after proxy failure").Default("1s").Duration()
	retryMaxWait     = kingpin.Flag("proxy.retry.max-wait", "Maximum amount of time to wait between proxy poll retries").Default("5s").Duration()
)

var (
	counterLabelFqdn = "fqdn"
	counterLabelList = []string{counterLabelFqdn}

	scrapeErrorCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushprox_client_scrape_errors_total",
			Help: "Number of scrape errors",
		}, counterLabelList,
	)
	pushErrorCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushprox_client_push_errors_total",
			Help: "Number of push errors",
		}, counterLabelList,
	)
	pollErrorCounter = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "pushprox_client_poll_errors_total",
			Help: "Number of poll errors",
		}, counterLabelList,
	)
)

func init() {
	prometheus.MustRegister(pushErrorCounter, pollErrorCounter, scrapeErrorCounter)
}

func newBackOffFromFlags() backoff.BackOff {
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = *retryInitialWait
	b.Multiplier = 1.5
	b.MaxInterval = *retryMaxWait
	b.MaxElapsedTime = time.Duration(0)
	return b
}

// Coordinator for scrape requests and responses
type Coordinator struct {
	logger log.Logger
}

func (c *Coordinator) handleErr(request *http.Request, client *http.Client, err error, newFqdn string) {
	level.Error(c.logger).Log("err", err)
	scrapeErrorCounter.With(prometheus.Labels{counterLabelFqdn: newFqdn}).Inc()
	resp := &http.Response{
		StatusCode: http.StatusInternalServerError,
		Body:       ioutil.NopCloser(strings.NewReader(err.Error())),
		Header:     http.Header{},
	}
	if err = c.doPush(resp, request, client); err != nil {
		pushErrorCounter.With(prometheus.Labels{counterLabelFqdn: newFqdn}).Inc()
		level.Warn(c.logger).Log("msg", "Failed to push failed scrape response:", "err", err)
		return
	}
	level.Info(c.logger).Log("msg", "Pushed failed scrape response")
}

func (c *Coordinator) doScrape(request *http.Request, client *http.Client, newFqdn string) {
	logger := log.With(c.logger, "scrape_id", request.Header.Get("id"))
	timeout, err := util.GetHeaderTimeout(request.Header)
	if err != nil {
		c.handleErr(request, client, err, newFqdn)
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

	if request.URL.Hostname() != newFqdn {
		c.handleErr(request, client, errors.New("scrape target doesn't match client fqdn"), newFqdn)
		return
	}

	scrapeResp, err := client.Do(request)
	if err != nil {
		msg := fmt.Sprintf("failed to scrape %s", request.URL.String())
		c.handleErr(request, client, errors.Wrap(err, msg), newFqdn)
		return
	}
	level.Info(logger).Log("msg", "Retrieved scrape response")
	if err = c.doPush(scrapeResp, request, client); err != nil {
		pushErrorCounter.With(prometheus.Labels{counterLabelFqdn: newFqdn}).Inc()
		level.Warn(logger).Log("msg", "Failed to push scrape response:", "err", err)
		return
	}
	level.Info(logger).Log("msg", "Pushed scrape result")
}

// Report the result of the scrape back up to the proxy.
func (c *Coordinator) doPush(resp *http.Response, origRequest *http.Request, client *http.Client) error {
	resp.Header.Set("id", origRequest.Header.Get("id")) // Link the request and response
	// Remaining scrape deadline.
	deadline, _ := origRequest.Context().Deadline()
	resp.Header.Set("X-Prometheus-Scrape-Timeout", fmt.Sprintf("%f", float64(time.Until(deadline))/1e9))

	base, err := url.Parse(*proxyURL)
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
		Body:          ioutil.NopCloser(buf),
		ContentLength: int64(buf.Len()),
	}
	request = request.WithContext(origRequest.Context())
	if _, err = client.Do(request); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) doPoll(client *http.Client, newFqdn string) error {
	base, err := url.Parse(*proxyURL)
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return errors.Wrap(err, "error parsing url")
	}
	u, err := url.Parse("poll")
	if err != nil {
		level.Error(c.logger).Log("msg", "Error parsing url:", "err", err)
		return errors.Wrap(err, "error parsing url poll")
	}
	url := base.ResolveReference(u)
	resp, err := client.Post(url.String(), "", strings.NewReader(newFqdn))
	if err != nil {
		level.Error(c.logger).Log("msg", "Error polling:", "Using-FQDN", newFqdn, "err", err)
		return errors.Wrap(err, "error polling")
	}
	defer resp.Body.Close()

	request, err := http.ReadRequest(bufio.NewReader(resp.Body))
	if err != nil {
		level.Error(c.logger).Log("msg", "Error reading request:", "Using-FQDN", newFqdn, "err", err)
		return errors.Wrap(err, "error reading request")
	}
	level.Info(c.logger).Log("msg", "Got scrape request", "scrape_id", request.Header.Get("id"), "url", request.URL)

	request.RequestURI = ""

	go c.doScrape(request, client, newFqdn)

	return nil
}

func (c *Coordinator) loop(bo backoff.BackOff, client *http.Client, newFqdn string) {
	op := func() error {
		return c.doPoll(client, newFqdn)
	}

	for {
		if err := backoff.RetryNotify(op, bo, func(err error, _ time.Duration) {
			pollErrorCounter.With(prometheus.Labels{counterLabelFqdn: newFqdn}).Inc()
		}); err != nil {
			level.Error(c.logger).Log("err", err)
		}
	}
}

func main() {
	promlogConfig := promlog.Config{}
	flag.AddFlags(kingpin.CommandLine, &promlogConfig)
	kingpin.HelpFlag.Short('h')
	kingpin.Parse()
	logger := promlog.New(&promlogConfig)
	coordinator := Coordinator{logger: logger}

	if *proxyURL == "" {
		level.Error(coordinator.logger).Log("msg", "--proxy-url flag must be specified.")
		os.Exit(1)
	}
	// Make sure proxyURL ends with a single '/'
	*proxyURL = strings.TrimRight(*proxyURL, "/") + "/"
	level.Info(coordinator.logger).Log("msg", "URL info", "proxy_url", *proxyURL)

	tlsConfig := &tls.Config{}
	if *tlsCert != "" {
		cert, err := tls.LoadX509KeyPair(*tlsCert, *tlsKey)
		if err != nil {
			level.Error(coordinator.logger).Log("msg", "Certificate or Key is invalid", "err", err)
			os.Exit(1)
		}

		// Setup HTTPS client
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if *caCertFile != "" {
		caCert, err := ioutil.ReadFile(*caCertFile)
		if err != nil {
			level.Error(coordinator.logger).Log("msg", "Not able to read cacert file", "err", err)
			os.Exit(1)
		}
		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM(caCert); !ok {
			level.Error(coordinator.logger).Log("msg", "Failed to use cacert file as ca certificate")
			os.Exit(1)
		}

		tlsConfig.RootCAs = caCertPool
	}

	if *metricsAddr != "" {
		go func() {
			if err := http.ListenAndServe(*metricsAddr, promhttp.Handler()); err != nil {
				level.Warn(coordinator.logger).Log("msg", "ListenAndServe", "err", err)
			}
		}()
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
			DualStack: true,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
	}

	client := &http.Client{Transport: transport}

	//Check if FQDN list is not passed - use single FDQN
	if *fqdnList == "" {
		coordinator.loop(newBackOffFromFlags(), client, *myFqdn)
	} else {
		fqdnLister := strings.Split(*fqdnList, ",")
		fqdnCount := len(fqdnLister)
		var wg sync.WaitGroup
		level.Info(coordinator.logger).Log("msg", "Polling in parallel for all FQDN", "FQDN Count", fqdnCount)
		for i := 0; i < fqdnCount; i++ {
			wg.Add(1)
			go coordinator.loop(newBackOffFromFlags(), client, fqdnLister[i])

		}
		wg.Wait()
	}

}
