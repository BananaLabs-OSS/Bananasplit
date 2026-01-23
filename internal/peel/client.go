package peel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

func (c *Client) SetRoute(playerIP, backend string) error {
	body, _ := json.Marshal(map[string]string{
		"player_ip": playerIP,
		"backend":   backend,
	})

	resp, err := c.http.Post(c.baseURL+"/routes", "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("peel request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("peel returned %d", resp.StatusCode)
	}

	return nil
}

func (c *Client) DeleteRoute(playerIP string) error {
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+"/routes/"+playerIP, nil)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("peel request failed: %w", err)
	}
	defer resp.Body.Close()

	return nil
}
