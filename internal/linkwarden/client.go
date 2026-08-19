// Package linkwarden talks to a user's Linkwarden instance through its v1 API.
package linkwarden

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const maxResponseBody = 2 << 20

type AuthMethod string

const (
	AuthCredentials AuthMethod = "credentials"
	AuthToken       AuthMethod = "token"
)

type Configuration struct {
	Enabled  bool
	URL      string
	Auth     AuthMethod
	Username string
	Password string
	Token    string
}

func (c Configuration) Validate() error {
	if !c.Enabled {
		return nil
	}
	base, err := url.Parse(strings.TrimSpace(c.URL))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return fmt.Errorf("Linkwarden address must be a complete URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return fmt.Errorf("Linkwarden address must use HTTP or HTTPS")
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("Linkwarden address cannot contain credentials, a query, or a fragment")
	}
	switch c.Auth {
	case AuthCredentials:
		if strings.TrimSpace(c.Username) == "" || c.Password == "" {
			return fmt.Errorf("Linkwarden username and password are required")
		}
	case AuthToken:
		if strings.TrimSpace(c.Token) == "" {
			return fmt.Errorf("Linkwarden API token is required")
		}
	default:
		return fmt.Errorf("choose a Linkwarden authentication method")
	}
	return nil
}

type Collection struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	ParentID *int64 `json:"parentId"`
}

type Tag struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type Link struct {
	URL          string
	Name         string
	Description  string
	CollectionID int64
	Tags         []Tag
}

type Client struct {
	httpClient *http.Client

	mu      sync.Mutex
	config  Configuration
	session string
}

func NewClient() *Client {
	return &Client{httpClient: &http.Client{Timeout: 15 * time.Second}}
}

func newClient(httpClient *http.Client) *Client {
	return &Client{httpClient: httpClient}
}

func (c *Client) Configure(configuration Configuration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.config != configuration {
		c.session = ""
	}
	c.config = configuration
}

func (c *Client) Collections(ctx context.Context) ([]Collection, error) {
	var response struct {
		Response []Collection `json:"response"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/collections", nil, &response); err != nil {
		return nil, err
	}
	sort.Slice(response.Response, func(i, j int) bool {
		return strings.ToLower(response.Response[i].Name) < strings.ToLower(response.Response[j].Name)
	})
	return response.Response, nil
}

func (c *Client) Tags(ctx context.Context) ([]Tag, error) {
	var tags []Tag
	path := "/api/v1/tags"
	for pages := 0; path != "" && pages < 100; pages++ {
		page, next, err := c.tagPage(ctx, path)
		if err != nil {
			return nil, err
		}
		tags = append(tags, page...)
		path = ""
		if next != nil {
			path = "/api/v1/tags?cursor=" + fmt.Sprint(*next)
		}
	}
	sort.Slice(tags, func(i, j int) bool { return strings.ToLower(tags[i].Name) < strings.ToLower(tags[j].Name) })
	return tags, nil
}

func (c *Client) tagPage(ctx context.Context, path string) ([]Tag, *int64, error) {
	var raw struct {
		Response json.RawMessage `json:"response"`
		Data     struct {
			Tags       []Tag  `json:"tags"`
			NextCursor *int64 `json:"nextCursor"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &raw); err != nil {
		return nil, nil, err
	}
	if len(raw.Response) == 0 {
		return raw.Data.Tags, raw.Data.NextCursor, nil
	}
	var legacy []Tag
	if err := json.Unmarshal(raw.Response, &legacy); err == nil {
		return legacy, nil, nil
	}
	var paged struct {
		Tags       []Tag  `json:"tags"`
		NextCursor *int64 `json:"nextCursor"`
	}
	if err := json.Unmarshal(raw.Response, &paged); err != nil {
		return nil, nil, fmt.Errorf("decode Linkwarden tags: %w", err)
	}
	return paged.Tags, paged.NextCursor, nil
}

func (c *Client) CreateLink(ctx context.Context, link Link) error {
	tags := make([]map[string]any, 0, len(link.Tags))
	for _, tag := range link.Tags {
		item := map[string]any{"name": tag.Name}
		if tag.ID > 0 {
			item["id"] = tag.ID
		}
		tags = append(tags, item)
	}
	body := map[string]any{
		"type": "url", "url": link.URL, "name": link.Name,
		"description": link.Description, "collection": map[string]any{"id": link.CollectionID},
		"tags": tags,
	}
	return c.do(ctx, http.MethodPost, "/api/v1/links", body, nil)
}

func (c *Client) Test(ctx context.Context) error {
	_, err := c.Collections(ctx)
	return err
}

func (c *Client) do(ctx context.Context, method, path string, body, destination any) error {
	token, baseURL, err := c.credentials(ctx)
	if err != nil {
		return err
	}
	var encoded io.Reader
	if body != nil {
		data, marshalErr := json.Marshal(body)
		if marshalErr != nil {
			return fmt.Errorf("encode Linkwarden request: %w", marshalErr)
		}
		encoded = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+path, encoded)
	if err != nil {
		return fmt.Errorf("build Linkwarden request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact Linkwarden: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return fmt.Errorf("read Linkwarden response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return apiError(response.StatusCode, data)
	}
	if destination != nil && len(data) > 0 {
		if err := json.Unmarshal(data, destination); err != nil {
			return fmt.Errorf("decode Linkwarden response: %w", err)
		}
	}
	return nil
}

func (c *Client) credentials(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	configuration := c.config
	if err := configuration.Validate(); err != nil {
		return "", "", err
	}
	baseURL := strings.TrimRight(strings.TrimSpace(configuration.URL), "/")
	if configuration.Auth == AuthToken {
		return strings.TrimSpace(configuration.Token), baseURL, nil
	}
	if c.session != "" {
		return c.session, baseURL, nil
	}
	body, err := json.Marshal(map[string]string{
		"username": configuration.Username, "password": configuration.Password,
		"sessionName": "Ziba",
	})
	if err != nil {
		return "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/session", bytes.NewReader(body))
	if err != nil {
		return "", "", fmt.Errorf("build Linkwarden login request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("log in to Linkwarden: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return "", "", fmt.Errorf("read Linkwarden login response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", "", apiError(response.StatusCode, data)
	}
	var result struct {
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", "", fmt.Errorf("Linkwarden login returned no session token")
	}
	var token string
	if err := json.Unmarshal(result.Response, &token); err != nil {
		var current struct {
			Token string `json:"token"`
		}
		if objectErr := json.Unmarshal(result.Response, &current); objectErr == nil {
			token = current.Token
		}
	}
	if token == "" {
		return "", "", fmt.Errorf("Linkwarden login returned no session token")
	}
	c.session = token
	return c.session, baseURL, nil
}

func apiError(status int, data []byte) error {
	var response struct {
		Response any `json:"response"`
	}
	message := ""
	if json.Unmarshal(data, &response) == nil {
		if text, ok := response.Response.(string); ok {
			message = strings.TrimSpace(text)
		}
	}
	if message == "" {
		message = strings.TrimSpace(string(data))
	}
	if message == "" {
		message = http.StatusText(status)
	}
	if len(message) > 300 {
		message = message[:300]
	}
	return errors.New("Linkwarden: " + message)
}
