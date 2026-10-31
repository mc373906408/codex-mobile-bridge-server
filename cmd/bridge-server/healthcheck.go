package main

import (
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/mc373906408/codex-mobile-bridge-server/internal/config"
)

func checkHealth(c config.Config) error {
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return apiStartupError("invalid health check listener")
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	default:
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return apiStartupError("health check requires a loopback or wildcard listener")
		}
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Host == "" {
		return apiStartupError("invalid health check origin")
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/readyz", nil)
	if err != nil {
		return apiStartupError("invalid health check request")
	}
	// Connect only locally, but retain the exact public Host required by the API.
	request.Host = origin.Host
	client := &http.Client{
		Timeout:   3 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return apiStartupError("health check unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return apiStartupError("health check not ready")
	}
	return nil
}
