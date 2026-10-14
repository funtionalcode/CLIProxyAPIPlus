package geminiweb

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	"golang.org/x/net/publicsuffix"
)

const Origin = "https://gemini.google.com"
const streamPath = "/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate"

var requestID atomic.Uint64
var bootstrapPattern = regexp.MustCompile(`"(SNlM0e|cfb2h|FdrFJe)":"((?:[^"\\]|\\.)*)"`)

type Credentials struct{ Secure1PSID, Secure1PSIDTS string }

// Client owns a request-local cookie jar. Cookie rotation cannot race across accounts.
type Client struct {
	http                  *http.Client
	baseURL               string
	token, build, session string
}

func NewClient(client *http.Client, credentials Credentials) (*Client, error) {
	if client == nil {
		return nil, &Error{500, "HTTP client is not configured"}
	}
	if strings.TrimSpace(credentials.Secure1PSID) == "" {
		return nil, &Error{401, "missing __Secure-1PSID; import a logged-in Gemini Web account"}
	}
	for _, value := range []string{credentials.Secure1PSID, credentials.Secure1PSIDTS} {
		if strings.ContainsAny(value, "\r\n;\x00") {
			return nil, &Error{401, "invalid session cookie"}
		}
		if err := (&http.Cookie{Name: "session", Value: value}).Valid(); err != nil {
			return nil, &Error{401, "invalid session cookie"}
		}
	}
	copyClient := *client
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	copyClient.Jar = jar
	copyClient.Timeout = 0
	copyClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	u, _ := url.Parse(Origin)
	jar.SetCookies(u, []*http.Cookie{
		{Name: "__Secure-1PSID", Value: credentials.Secure1PSID, Path: "/", Secure: true, HttpOnly: true},
		{Name: "__Secure-1PSIDTS", Value: credentials.Secure1PSIDTS, Path: "/", Secure: true, HttpOnly: true},
	})
	return &Client{http: &copyClient, baseURL: Origin}, nil
}

func (c *Client) Credentials() Credentials {
	u, _ := url.Parse(Origin)
	var result Credentials
	for _, cookie := range c.http.Jar.Cookies(u) {
		switch cookie.Name {
		case "__Secure-1PSID":
			result.Secure1PSID = cookie.Value
		case "__Secure-1PSIDTS":
			result.Secure1PSIDTS = cookie.Value
		}
	}
	return result
}

func (c *Client) do(req *http.Request) (*http.Response, error) {
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Origin", Origin)
	req.Header.Set("Referer", Origin+"/")
	response, err := c.http.Do(req)
	if err != nil {
		if req.Context().Err() != nil {
			return nil, req.Context().Err()
		}
		// net/url errors can contain private session query parameters.
		return nil, &Error{502, "upstream connection failed"}
	}
	return response, nil
}

func checkStatus(status int) error {
	if status >= 200 && status < 300 {
		return nil
	}
	if status == 301 || status == 302 || status == 303 || status == 307 || status == 308 || status == 401 || status == 403 {
		return &Error{401, "web session expired or access denied; log in and import cookies again"}
	}
	return &Error{status, "upstream HTTP status " + strconv.Itoa(status)}
}

// Bootstrap fetches fresh form tokens using only the supplied account cookies.
func (c *Client) Bootstrap(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/app", nil)
	if err != nil {
		return err
	}
	resp, err := c.do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp.Body)
	if err = checkStatus(resp.StatusCode); err != nil {
		return err
	}
	const limit = 16 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return err
	}
	if len(body) > limit {
		return &Error{502, "bootstrap page exceeds size limit"}
	}
	for _, match := range bootstrapPattern.FindAllSubmatch(body, -1) {
		var value string
		if json.Unmarshal(append(append([]byte{'"'}, match[2]...), '"'), &value) != nil {
			continue
		}
		switch string(match[1]) {
		case "SNlM0e":
			c.token = value
		case "cfb2h":
			c.build = value
		case "FdrFJe":
			c.session = value
		}
	}
	if c.token == "" || c.build == "" || c.session == "" {
		return &Error{401, "could not initialize a logged-in web session; import fresh cookies"}
	}
	return nil
}

// Generate only adds transport framing around the final business payload.
func (c *Client) Generate(ctx context.Context, modelID string, payload []byte) (*http.Response, error) {
	if !json.Valid(payload) {
		return nil, &Error{400, "invalid final web payload"}
	}
	if c.token == "" {
		return nil, &Error{401, "web session is not initialized"}
	}
	envelope, err := json.Marshal([]any{nil, string(payload)})
	if err != nil {
		return nil, err
	}
	form := url.Values{"at": {c.token}, "f.req": {string(envelope)}}
	query := url.Values{"bl": {c.build}, "f.sid": {c.session}, "hl": {"zh-CN"}, "_reqid": {strconv.FormatUint(requestID.Add(100000), 10)}, "rt": {"c"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+streamPath+"?"+query.Encode(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	header, _ := json.Marshal([]any{1, nil, nil, nil, modelID})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	req.Header.Set("X-Same-Domain", "1")
	req.Header.Set("X-Goog-Ext-525001261-Jspb", string(header))
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	if err = checkStatus(resp.StatusCode); err != nil {
		closeBody(resp.Body)
		return nil, err
	}
	return resp, nil
}

func closeBody(body io.ReadCloser) {
	if errClose := body.Close(); errClose != nil {
		log.WithError(errClose).Debug("gemini-web: close response")
	}
}
