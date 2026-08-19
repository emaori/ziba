package linkwarden

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func jsonResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestCredentialsAreExchangedOnceAndCreateLinkUsesOfficialShape(t *testing.T) {
	logins := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/api/v1/session":
			logins++
			return jsonResponse(`{"response":{"token":"session-token"}}`), nil
		case "/api/v1/collections":
			if r.Header.Get("Authorization") != "Bearer session-token" {
				t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			}
			return jsonResponse(`{"response":[{"id":2,"name":"Reading"}]}`), nil
		case "/api/v1/links":
			var body struct {
				URL, Name, Description string
				Collection             struct {
					ID int64 `json:"id"`
				}
				Tags []Tag
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.URL != "https://example.com/article" || body.Collection.ID != 2 || len(body.Tags) != 2 || body.Tags[1].Name != "new" {
				t.Errorf("request = %+v", body)
			}
			return jsonResponse(`{"response":{"id":9}}`), nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Header: make(http.Header)}, nil
		}
	})}

	client := newClient(httpClient)
	client.Configure(Configuration{Enabled: true, URL: "https://links.example", Auth: AuthCredentials, Username: "reader", Password: "password"})
	if _, err := client.Collections(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := client.CreateLink(context.Background(), Link{URL: "https://example.com/article", Name: "Article", Description: "Summary", CollectionID: 2, Tags: []Tag{{ID: 3, Name: "old"}, {Name: "new"}}}); err != nil {
		t.Fatal(err)
	}
	if logins != 1 {
		t.Errorf("login calls = %d, want 1", logins)
	}
}

func TestCredentialsAcceptLegacyStringSessionResponse(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/v1/session" {
			return jsonResponse(`{"response":"legacy-token"}`), nil
		}
		if r.Header.Get("Authorization") != "Bearer legacy-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		return jsonResponse(`{"response":[]}`), nil
	})}
	client := newClient(httpClient)
	client.Configure(Configuration{Enabled: true, URL: "https://links.example", Auth: AuthCredentials, Username: "reader", Password: "password"})
	if err := client.Test(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestTagsAcceptLegacyAndPaginatedResponses(t *testing.T) {
	for name, payloads := range map[string][]string{
		"legacy": {`{"response":[{"id":1,"name":"Go"}]}`},
		"paged": {
			`{"response":{"tags":[{"id":2,"name":"AI"}],"nextCursor":3}}`,
			`{"response":{"tags":[{"id":3,"name":"Go"}],"nextCursor":null}}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			calls := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				response := jsonResponse(payloads[calls])
				calls++
				return response, nil
			})}
			client := newClient(httpClient)
			client.Configure(Configuration{Enabled: true, URL: "https://links.example", Auth: AuthToken, Token: "token"})
			tags, err := client.Tags(context.Background())
			if err != nil || len(tags) != len(payloads) {
				t.Fatalf("tags = %+v, err = %v", tags, err)
			}
		})
	}
}
