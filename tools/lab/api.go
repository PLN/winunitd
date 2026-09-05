package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type api struct {
	base   string
	token  string
	client *http.Client
}

type apiStatusError struct{ Code int }

func (e apiStatusError) Error() string { return fmt.Sprintf("API request failed with HTTP %d", e.Code) }

func newAPI(base, caFile, tokenFile string) (*api, error) {
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("API origin must be an HTTPS origin without credentials, path, query, or fragment")
	}
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("read private cluster CA: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("invalid cluster CA")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, fmt.Errorf("read private API token: %w", err)
	}
	auth := strings.TrimSpace(string(token))
	if !strings.HasPrefix(auth, "PVEAPIToken=") || strings.ContainsAny(auth, "\r\n") {
		return nil, fmt.Errorf("invalid API token file")
	}
	return &api{base: strings.TrimSuffix(base, "/"), token: auth, client: &http.Client{
		Timeout:       30 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("API redirects are forbidden") },
	}}, nil
}

func (a *api) call(ctx context.Context, method, path string, form url.Values, result any) error {
	if method == http.MethodGet && len(form) != 0 {
		return a.request(ctx, method, path+"?"+form.Encode(), nil, "", result)
	}
	return a.request(ctx, method, path, strings.NewReader(form.Encode()), "application/x-www-form-urlencoded", result)
}

func (a *api) callJSON(ctx context.Context, method, path string, body any, result any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return a.request(ctx, method, path, bytes.NewReader(data), "application/json", result)
}

func (a *api) request(ctx context.Context, method, path string, body io.Reader, contentType string, result any) error {
	if !strings.HasPrefix(path, "/") || strings.Contains(path, "..") || strings.Contains(path, "#") {
		return fmt.Errorf("invalid API path")
	}
	req, err := http.NewRequestWithContext(ctx, method, a.base+"/api2/json"+path, body)
	if err != nil {
		return fmt.Errorf("create API request")
	}
	req.Header.Set("Authorization", a.token)
	req.Header.Set("Content-Type", contentType)
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("API transport failed (%T); inspect private controller configuration", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return apiStatusError{Code: resp.StatusCode}
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("invalid API response")
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, result); err != nil {
		return fmt.Errorf("invalid API result: %w", err)
	}
	return nil
}

func (a *api) waitTask(ctx context.Context, node, task string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var status struct {
			Status     string `json:"status"`
			ExitStatus string `json:"exitstatus"`
		}
		if err := a.call(ctx, http.MethodGet, "/nodes/"+node+"/tasks/"+url.PathEscape(task)+"/status", nil, &status); err != nil {
			return err
		}
		if status.Status == "stopped" {
			if status.ExitStatus != "OK" {
				return fmt.Errorf("cluster task failed; inspect private task log")
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
