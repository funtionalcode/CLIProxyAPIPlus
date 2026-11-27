package helps

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/protobuf/encoding/protowire"
)

type telemetryTestTransport func(*http.Request) (*http.Response, error)

func (f telemetryTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func telemetryTestAttempt(mode string, client *http.Client) *AntigravityTelemetryAttempt {
	cfg := &config.Config{}
	cfg.Antigravity.Telemetry.Mode = mode
	headers := http.Header{"User-Agent": {"antigravity/hub/2.30.1 (aidev_client; os_type=linux; arch=amd64)"}}
	return BeginAntigravityTelemetry(cfg, &cliproxyauth.Auth{ID: "private-account@example.com"}, "secret-token", headers,
		[]byte(`{"model":"gemini-test","request":{"labels":{"cascade_id":"cascade","trajectory_id":"trajectory","last_step_index":"2"},"contents":[{"parts":[{"text":"private-prompt"}]}],"tools":[{"secret":"private-tool"}]}}`), client)
}

const telemetryTestResponse = `{"response":{"responseId":"response-id","modelVersion":"gemini-version","candidates":[{"finishReason":"STOP","content":{"parts":[{"text":"private-answer"},{"thought":true,"text":"private-thought"}]}}],"usageMetadata":{"promptTokenCount":20,"candidatesTokenCount":7,"thoughtsTokenCount":3,"cachedContentTokenCount":5,"totalTokenCount":30}}}`

func TestAntigravityTelemetryModesAndPrivacy(t *testing.T) {
	previousHooks := log.StandardLogger().ReplaceHooks(make(log.LevelHooks))
	defer log.StandardLogger().ReplaceHooks(previousHooks)
	hook := logtest.NewGlobal()
	for _, tc := range []struct {
		mode     string
		disabled bool
	}{{"", false}, {"preview", false}, {"off", true}, {"typo-send", true}} {
		t.Run(tc.mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				client := &http.Client{Transport: telemetryTestTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("should not send") })}
				a := telemetryTestAttempt(tc.mode, client)
				if (a == nil) != tc.disabled {
					t.Fatalf("attempt enabled for mode %q: %v", tc.mode, a)
				}
				hook.Reset()
				a.Observe([]byte(telemetryTestResponse))
				a.Finish(200, nil)
				a.Finish(200, nil)
				synctest.Wait()
				if calls != 0 {
					t.Fatal("preview/off sent a network request")
				}
				entries := hook.AllEntries()
				if tc.disabled {
					if len(entries) != 0 {
						t.Fatal("off mode logged an event")
					}
					return
				}
				if len(entries) != 1 {
					t.Fatalf("expected exactly one preview, got %d", len(entries))
				}
				preview := entries[0].Data["event"].(string)
				assertTelemetryPrivateDataAbsent(t, []byte(preview))
				if ojson.Get(preview, "outcome").String() != "completed" || ojson.Get(preview, "usage.input_tokens").Uint() != 20 {
					t.Fatalf("actual response metadata missing: %s", preview)
				}
			})
		})
	}
	if a := BeginAntigravityTelemetry(nil, &cliproxyauth.Auth{Disabled: true}, "token", nil, nil, nil); a != nil {
		t.Fatal("disabled credential created telemetry")
	}
}

func assertTelemetryPrivateDataAbsent(t *testing.T, data []byte) {
	t.Helper()
	for _, secret := range []string{"private-prompt", "private-answer", "private-thought", "private-tool", "secret-token", "private-account@example.com"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("telemetry includes private field %s", secret)
		}
	}
}

func TestAntigravityTelemetrySendWireAndRedirect(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTemporaryRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var bodies [][]byte
				client := &http.Client{Transport: telemetryTestTransport(func(r *http.Request) (*http.Response, error) {
					if r.URL.String() != antigravityTelemetryEndpoint || r.Header.Get("Authorization") != "Bearer secret-token" || r.Header.Get("Content-Type") != "application/octet-stream" {
						t.Error("wrong telemetry destination or transport headers")
					}
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						return nil, errRead
					}
					bodies = append(bodies, body)
					return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://redirect.invalid/log"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
				})}
				a := telemetryTestAttempt("send", client)
				a.Observe([]byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":1}}`))
				a.Observe([]byte(telemetryTestResponse))
				a.Finish(200, nil)
				a.Finish(200, nil)
				synctest.Wait()
				if len(bodies) != 1 {
					t.Fatalf("expected one send without redirect, got %d", len(bodies))
				}
				assertTelemetryPrivateDataAbsent(t, bodies[0])
				request := decodeTelemetryFields(t, bodies[0])
				if request[2].(uint64) != 2747 {
					t.Fatal("wrong log source")
				}
				event := decodeTelemetryFields(t, request[3].([]byte))
				extension := decodeTelemetryFields(t, event[6].([]byte))
				if string(extension[17].([]byte)) != "2.30.1" || string(extension[18].([]byte)) != "linux" || extension[24].(uint64) != 1 {
					t.Fatal("wire profile/redaction mismatch")
				}
				generator := decodeTelemetryFields(t, extension[3].([]byte))
				if string(generator[1].([]byte)) != "trajectory" || string(generator[2].([]byte)) != "cascade" || string(generator[12].([]byte)) != "gemini-test" {
					t.Fatal("request identity mismatch")
				}
				step := decodeTelemetryFields(t, generator[3].([]byte))
				index, n := protowire.ConsumeVarint(step[2].([]byte))
				if n <= 0 || index != 2 {
					t.Fatal("wrong packed step")
				}
				chat := decodeTelemetryFields(t, step[1].([]byte))
				if string(chat[19].([]byte)) != "gemini-version" {
					t.Fatal("response model lost")
				}
				usage := decodeTelemetryFields(t, chat[4].([]byte))
				for field, want := range map[protowire.Number]uint64{2: 20, 3: 10, 5: 5, 9: 3, 10: 7} {
					if usage[field].(uint64) != want {
						t.Errorf("usage field %d = %v, want %d", field, usage[field], want)
					}
				}
				if string(usage[11].([]byte)) != "response-id" {
					t.Fatal("response id lost")
				}
				if client.CheckRedirect != nil {
					t.Fatal("send mutated caller HTTP client")
				}
			})
		})
	}
}

func decodeTelemetryFields(t *testing.T, raw []byte) map[protowire.Number]any {
	t.Helper()
	fields := make(map[protowire.Number]any)
	for len(raw) > 0 {
		number, kind, n := protowire.ConsumeTag(raw)
		if n < 0 {
			t.Fatal("invalid protobuf tag")
		}
		raw = raw[n:]
		switch kind {
		case protowire.VarintType:
			value, nValue := protowire.ConsumeVarint(raw)
			if nValue < 0 {
				t.Fatal("invalid protobuf varint")
			}
			fields[number] = value
			raw = raw[nValue:]
		case protowire.BytesType:
			value, nValue := protowire.ConsumeBytes(raw)
			if nValue < 0 {
				t.Fatal("invalid protobuf bytes")
			}
			fields[number] = value
			raw = raw[nValue:]
		default:
			t.Fatalf("unexpected wire type %d", kind)
		}
	}
	return fields
}

func TestAntigravityTelemetryDoesNotSendIncompleteOrFailed(t *testing.T) {
	for _, tc := range []struct {
		name, payload string
		status        int
		err           error
		outcome       string
	}{
		{"incomplete", `{"response":{"usageMetadata":{"candidatesTokenCount":1}}}`, 200, nil, "incomplete"},
		{"cancelled", telemetryTestResponse, 200, context.Canceled, "cancelled"},
		{"transport", telemetryTestResponse, 200, io.ErrUnexpectedEOF, "failed"},
		{"status", telemetryTestResponse, 403, nil, "failed"},
		{"embedded-error", `{"response":{"error":{"message":"private-answer"}}}`, 200, nil, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				a := telemetryTestAttempt("send", &http.Client{Transport: telemetryTestTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected send") })})
				a.Observe([]byte(tc.payload))
				a.Finish(tc.status, tc.err)
				synctest.Wait()
				if calls != 0 || a.event.Outcome != tc.outcome {
					t.Fatalf("calls=%d outcome=%s", calls, a.event.Outcome)
				}
			})
		})
	}
}

func TestAntigravityTelemetryDeliveryBound(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		var calls atomic.Int32
		client := &http.Client{Transport: telemetryTestTransport(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			<-release
			return nil, errors.New("test delivery failure")
		})}
		for range 10 {
			a := telemetryTestAttempt("send", client)
			a.Observe([]byte(telemetryTestResponse))
			a.Finish(200, nil)
		}
		synctest.Wait()
		if int(calls.Load()) != cap(antigravityTelemetrySlots) {
			t.Errorf("delivery concurrency=%d, want %d", calls.Load(), cap(antigravityTelemetrySlots))
		}
		close(release)
		synctest.Wait()
		if len(antigravityTelemetrySlots) != 0 {
			t.Fatal("delivery slots leaked")
		}
	})
}
