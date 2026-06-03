package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/BananaLabs-OSS/Fiber/pulp"
)

// peelFetchTimeout matches potassium/relay.NewClient's &http.Client{Timeout: 5 * time.Second}.
// Peel SetRoute/DeleteRoute run inline from /route-request and /players handlers; a slow
// Peel must not stall the HTTP handler past the matcher's budget.
const peelFetchTimeout = 5 * time.Second

// PeelClient talks to a Peel relay service over HTTP. All requests
// route through pulp.HTTP.Fetch — the cell never touches net/http.
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

// SetRoute installs a player_ip -> backend mapping in Peel's route table.
// backend is assembled from a registry-supplied Host:Port (see main.go
// /route-request and matcher.go). That value is internal-trusted
// (Bananagine-sourced) and is not dialed by this cell — it is forwarded
// into Peel, which applies first-write backend validation on its side
// (commit 80e9fe2). Egress/format validation is therefore deferred to
// Peel rather than duplicated here; tighten here only if the registry
// trust boundary weakens.
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
		Timeout: peelFetchTimeout,
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
		Method:  "DELETE",
		URL:     c.baseURL + "/routes/" + playerIP,
		Timeout: peelFetchTimeout,
	})
	if err != nil {
		return fmt.Errorf("peel delete route: %w", err)
	}
	if resp.Status != 200 && resp.Status != 204 {
		return fmt.Errorf("peel returned %d", resp.Status)
	}
	return nil
}
