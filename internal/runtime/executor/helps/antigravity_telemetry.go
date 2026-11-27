package helps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"google.golang.org/protobuf/encoding/protowire"
)

const antigravityTelemetryEndpoint = "https://play.googleapis.com/log"

// Bound detached delivery across executor replacements as well as accounts.
// Busy delivery is dropped rather than blocking inference or retaining tokens
// in an unbounded queue. There are no additional upstream network deadlines.
var antigravityTelemetrySlots = make(chan struct{}, 4)

type antigravityTelemetryUsage struct {
	Input    *uint64 `json:"input_tokens,omitempty"`
	Output   *uint64 `json:"response_output_tokens,omitempty"`
	Thinking *uint64 `json:"thinking_output_tokens,omitempty"`
	Cached   *uint64 `json:"cache_read_tokens,omitempty"`
}

// The event is an allowlist, not a redacted copy of a request or response.
type antigravityTelemetryEvent struct {
	Credential    string                    `json:"credential_ref"`
	TimeMillis    int64                     `json:"event_time_ms"`
	Model         string                    `json:"model,omitempty"`
	ResponseModel string                    `json:"response_model,omitempty"`
	ResponseID    string                    `json:"response_id,omitempty"`
	Cascade       string                    `json:"cascade_id,omitempty"`
	Trajectory    string                    `json:"trajectory_id,omitempty"`
	Step          *uint64                   `json:"step_index,omitempty"`
	Version       string                    `json:"hub_version,omitempty"`
	OS            string                    `json:"os,omitempty"`
	Arch          string                    `json:"arch,omitempty"`
	Usage         antigravityTelemetryUsage `json:"usage"`
	HTTPStatus    int                       `json:"http_status"`
	Outcome       string                    `json:"outcome"`
}

// AntigravityTelemetryAttempt is owned by the goroutine consuming one response.
// Only the token and transport are retained for optional delivery; neither is
// part of the preview or serialized event.
type AntigravityTelemetryAttempt struct {
	event    antigravityTelemetryEvent
	mode     string
	token    string
	client   *http.Client
	terminal bool
	failed   bool
	finished bool
}

// BeginAntigravityTelemetry snapshots finalized wire metadata. Preview is the
// default; disabled credentials and off mode do not create an attempt.
func BeginAntigravityTelemetry(cfg *config.Config, auth *cliproxyauth.Auth, token string, headers http.Header, payload []byte, client *http.Client) *AntigravityTelemetryAttempt {
	mode := (config.AntigravityTelemetryConfig{}).EffectiveMode()
	if cfg != nil {
		mode = cfg.Antigravity.Telemetry.EffectiveMode()
	}
	if mode == "off" || auth == nil || auth.Disabled {
		return nil
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	profile := misc.ParseAntigravityDeviceProfile(headers.Get("User-Agent"))
	identity := auth.ID
	if identity == "" {
		identity = auth.FileName
	}
	digest := sha256.Sum256([]byte("cli-proxy-api:antigravity:telemetry\x00" + identity))
	attempt := &AntigravityTelemetryAttempt{mode: mode, token: token, client: client, event: antigravityTelemetryEvent{
		Credential: fmt.Sprintf("%x", digest[:8]), TimeMillis: time.Now().UnixMilli(),
		Model:      telemetryText(root.Get("model").String()),
		Cascade:    telemetryText(root.Get("request.labels.cascade_id").String()),
		Trajectory: telemetryText(root.Get("request.labels.trajectory_id").String()),
		Version:    telemetryText(profile.Version), OS: telemetryText(profile.OS), Arch: telemetryText(profile.Arch),
	}}
	if value, errParse := strconv.ParseUint(root.Get("request.labels.last_step_index").String(), 10, 32); errParse == nil {
		attempt.event.Step = new(value)
	}
	return attempt
}

func telemetryText(value string) string {
	if len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
		return ""
	}
	return strings.Clone(value)
}

// Observe consumes an already reassembled upstream JSON event before translation.
// SSE token counts are cumulative; the latest value replaces the previous one.
func (a *AntigravityTelemetryAttempt) Observe(payload []byte) {
	if a == nil || a.finished {
		return
	}
	root := util.ParseGJSONBytesNoCopy(payload)
	if root.Get("error").Exists() {
		a.failed = true
		return
	}
	if response := root.Get("response"); response.Exists() {
		root = response
	}
	if root.Get("error").Exists() {
		a.failed = true
		return
	}
	if value := telemetryText(root.Get("modelVersion").String()); value != "" {
		a.event.ResponseModel = value
	}
	if value := telemetryText(root.Get("responseId").String()); value != "" {
		a.event.ResponseID = value
	}
	usage := root.Get("usageMetadata")
	for _, field := range []struct {
		name string
		dest **uint64
	}{
		{"promptTokenCount", &a.event.Usage.Input},
		{"candidatesTokenCount", &a.event.Usage.Output},
		{"thoughtsTokenCount", &a.event.Usage.Thinking},
		{"cachedContentTokenCount", &a.event.Usage.Cached},
	} {
		value := usage.Get(field.name)
		if value.Type != ojson.Number {
			continue
		}
		if count, errParse := strconv.ParseUint(value.Raw, 10, 64); errParse == nil && count < 1<<53 {
			*field.dest = new(count)
		}
	}
	for _, candidate := range root.Get("candidates").Array() {
		if reason := candidate.Get("finishReason").String(); reason != "" {
			a.terminal = true
		}
	}
}

// Finish records a local preview and optionally schedules one best-effort send.
// A Google acknowledgement is delivery status, not proof of event ingestion.
func (a *AntigravityTelemetryAttempt) Finish(status int, err error) {
	if a == nil || a.finished {
		return
	}
	a.finished = true
	a.event.HTTPStatus = status
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		a.event.Outcome = "cancelled"
	case err != nil || a.failed || status < 200 || status >= 300:
		a.event.Outcome = "failed"
	case a.terminal:
		a.event.Outcome = "completed"
	default:
		a.event.Outcome = "incomplete"
	}
	raw, _ := json.Marshal(a.event)
	log.WithFields(log.Fields{"component": "antigravity_telemetry", "mode": a.mode, "event": string(raw)}).Info("antigravity telemetry preview")
	if a.mode != "send" || a.event.Outcome != "completed" || a.token == "" || a.client == nil {
		return
	}
	select {
	case antigravityTelemetrySlots <- struct{}{}:
	default:
		log.WithField("component", "antigravity_telemetry").Warn("telemetry delivery busy; event dropped")
		return
	}
	// Snapshot the allowlisted event. No request or response body enters the goroutine.
	event, token, client := a.event, a.token, *a.client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	go func() {
		defer func() { <-antigravityTelemetrySlots }()
		request, errRequest := http.NewRequestWithContext(context.Background(), http.MethodPost, antigravityTelemetryEndpoint, bytes.NewReader(encodeAntigravityTelemetry(event)))
		if errRequest != nil {
			log.WithField("component", "antigravity_telemetry").Warn("telemetry request construction failed")
			return
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("User-Agent", "Go-http-client/2.0")
		response, errSend := client.Do(request)
		if errSend != nil {
			log.WithField("component", "antigravity_telemetry").Warn("telemetry transport failed")
			return
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		if errClose := response.Body.Close(); errClose != nil {
			log.WithField("component", "antigravity_telemetry").Debug("telemetry response close failed")
		}
		log.WithFields(log.Fields{"component": "antigravity_telemetry", "http_status": response.StatusCode}).Info("telemetry delivery finished")
	}()
}

// These field numbers describe the observed Hub generation-event subset. No
// generic map-to-protobuf conversion is allowed: unknown fields stay local.
func encodeAntigravityTelemetry(event antigravityTelemetryEvent) []byte {
	var usage []byte
	for _, field := range []struct {
		number protowire.Number
		value  *uint64
	}{{2, event.Usage.Input}, {5, event.Usage.Cached}, {9, event.Usage.Thinking}, {10, event.Usage.Output}} {
		if field.value != nil {
			usage = telemetryVarint(usage, field.number, *field.value)
		}
	}
	if event.Usage.Output != nil {
		total := *event.Usage.Output
		if event.Usage.Thinking != nil {
			total += *event.Usage.Thinking
		}
		usage = telemetryVarint(usage, 3, total)
	}
	usage = telemetryTextField(usage, 11, event.ResponseID)
	chat := telemetryBytes(nil, 4, usage)
	chat = telemetryTextField(chat, 19, event.ResponseModel)
	step := telemetryBytes(nil, 1, chat)
	if event.Step != nil {
		step = telemetryBytes(step, 2, protowire.AppendVarint(nil, *event.Step))
	}
	generator := telemetryTextField(nil, 1, event.Trajectory)
	generator = telemetryTextField(generator, 2, event.Cascade)
	generator = telemetryBytes(generator, 3, step)
	generator = telemetryTextField(generator, 12, event.Model)
	extension := telemetryBytes(nil, 3, generator)
	extension = telemetryTextField(extension, 16, "antigravity")
	extension = telemetryTextField(extension, 17, event.Version)
	extension = telemetryTextField(extension, 18, event.OS)
	extension = telemetryTextField(extension, 19, event.Arch)
	extension = telemetryVarint(extension, 24, 1)
	extension = telemetryTextField(extension, 27, "hub")
	logEvent := telemetryVarint(nil, 1, uint64(event.TimeMillis))
	logEvent = telemetryBytes(logEvent, 6, extension)
	request := telemetryBytes(nil, 1, telemetryVarint(nil, 1, 16))
	request = telemetryVarint(request, 2, 2747)
	request = telemetryBytes(request, 3, logEvent)
	return telemetryVarint(request, 4, uint64(time.Now().UnixMilli()))
}

func telemetryBytes(dst []byte, field protowire.Number, value []byte) []byte {
	return protowire.AppendBytes(protowire.AppendTag(dst, field, protowire.BytesType), value)
}

func telemetryTextField(dst []byte, field protowire.Number, value string) []byte {
	if value == "" {
		return dst
	}
	return telemetryBytes(dst, field, []byte(value))
}

func telemetryVarint(dst []byte, field protowire.Number, value uint64) []byte {
	return protowire.AppendVarint(protowire.AppendTag(dst, field, protowire.VarintType), value)
}
