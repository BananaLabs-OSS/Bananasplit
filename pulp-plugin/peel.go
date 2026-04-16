package main

import (
	"encoding/json"
	"fmt"

	"github.com/BananaLabs-OSS/Fiber/pulp"
)

// PeelClient talks to a Peel relay service over HTTP. All requests
// route through pulp.HTTP.Fetch — the plugin never touches net/http.
type PeelClient struct {
	baseURL string
}

// NewPeelClient returns a client bound to baseURL. Empty baseURL
// marks the client as disabled; methods become no-ops.
func NewPeelClient(baseURL string) *PeelClient {
	return &PeelClient{baseURL: baseURL}
}

// Enabled reports whether a Peel URL was configured.
func (c *PeelClient) Enabled() bool { return c != nil && c.baseURL != "" }

func (c *PeelClient) SetRoute(playerIP, backend string) error {
	if !c.Enabled() {
		return nil
	}
	body, _ := json.Marshal(map[string]string{
		"player_ip": playerIP,
		"backend":   backend,
	})
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method:  "POST",
		URL:     c.baseURL + "/routes",
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    body,
	})
	if err != nil {
		return fmt.Errorf("peel set route: %w", err)
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return fmt.Errorf("peel returned %d", resp.Status)
	}
	return nil
}

func (c *PeelClient) DeleteRoute(playerIP string) error {
	if !c.Enabled() {
		return nil
	}
	resp, err := pulp.HTTP.Fetch(pulp.HTTPFetchRequest{
		Method: "DELETE",
		URL:    c.baseURL + "/routes/" + playerIP,
	})
	if err != nil {
		return fmt.Errorf("peel delete route: %w", err)
	}
	if resp.Status != 200 && resp.Status != 204 {
		return fmt.Errorf("peel returned %d", resp.Status)
	}
	return nil
}
