package bsky

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mdfranz/bskyban/internal/termtext"
)

type Client struct {
	base   string
	http   *http.Client
	jwt    string
	DID    string
	Handle string // your handle as the server reports it
}

func NewClient(base string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, nsid string, query url.Values, body, out any) error {
	u := c.base + "/xrpc/" + nsid
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.jwt != "" {
		req.Header.Set("Authorization", "Bearer "+c.jwt)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error, Message string }
		_ = json.Unmarshal(data, &e)
		if e.Error != "" {
			return fmt.Errorf("%s: %s (%s)", nsid, termtext.Sanitize(e.Message), termtext.Sanitize(e.Error))
		}
		return fmt.Errorf("%s: HTTP %d", nsid, resp.StatusCode)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func (c *Client) Login(ctx context.Context, identifier, password string) error {
	var out struct {
		AccessJwt string `json:"accessJwt"`
		DID       string `json:"did"`
		Handle    string `json:"handle"`
	}
	err := c.do(ctx, http.MethodPost, "com.atproto.server.createSession", nil,
		map[string]string{"identifier": identifier, "password": password}, &out)
	if err != nil {
		return err
	}
	c.jwt, c.DID, c.Handle = out.AccessJwt, out.DID, out.Handle
	return nil
}
