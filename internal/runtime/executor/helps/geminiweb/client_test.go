package geminiweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestClientBootstrapCookiesFramingAndIsolation(t *testing.T) {
	count := 0
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		count++
		cookie, err := r.Cookie("__Secure-1PSID")
		if err != nil || cookie.Value != "account-a" {
			t.Fatalf("missing account cookie")
		}
		body := `{"SNlM0e":"form-token","cfb2h":"web-build","FdrFJe":"session"}`
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			var envelope []json.RawMessage
			if err := json.Unmarshal([]byte(r.Form.Get("f.req")), &envelope); err != nil {
				t.Fatal(err)
			}
			var payload string
			if err := json.Unmarshal(envelope[1], &payload); err != nil {
				t.Fatal(err)
			}
			if payload != `[["configured"]]` {
				t.Fatalf("final payload was modified: %s", payload)
			}
			if r.Form.Get("at") != "form-token" || r.URL.Query().Get("f.sid") != "session" {
				t.Fatal("missing transport tokens")
			}
			if r.Header.Get("X-Goog-Ext-525001261-Jspb") != `[1,null,null,null,"mode-id"]` {
				t.Fatal("wrong model header")
			}
			body = testFrame("ok", true)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	original := &http.Client{Transport: transport, Timeout: time.Second}
	client, err := NewClient(original, Credentials{Secure1PSID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	if client.http.Timeout != 0 || original.Timeout != time.Second || original.Jar != nil {
		t.Fatal("client mutated shared policy")
	}
	if err = client.Bootstrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, err := client.Generate(context.Background(), "mode-id", []byte(`[["configured"]]`))
	if err != nil {
		t.Fatal(err)
	}
	if err = resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("requests=%d", count)
	}
	other, err := NewClient(original, Credentials{Secure1PSID: "account-b"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Credentials().Secure1PSID != "account-b" || client.Credentials().Secure1PSID != "account-a" {
		t.Fatal("cookie jars share state")
	}
}

func TestClientDoesNotFollowLoginRedirectsOrExposeResponse(t *testing.T) {
	for _, status := range []int{302, 401, 403, 429} {
		calls := 0
		client, err := NewClient(&http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://accounts.google.com/"}}, Body: io.NopCloser(strings.NewReader("PRIVATE-RESPONSE")), Request: r}, nil
		})}, Credentials{Secure1PSID: "cookie"})
		if err != nil {
			t.Fatal(err)
		}
		err = client.Bootstrap(context.Background())
		if err == nil || strings.Contains(err.Error(), "PRIVATE") || calls != 1 {
			t.Fatalf("status=%d calls=%d err=%v", status, calls, err)
		}
	}
}
