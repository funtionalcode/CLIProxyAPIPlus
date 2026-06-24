package handlers

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	log "github.com/sirupsen/logrus"
)

type StreamForwardOptions struct {
	// Model is used only for temporary disconnect diagnostics.
	Model string

	// InitialChunkCount accounts for chunks written before entering ForwardStream.
	InitialChunkCount int

	// KeepAliveInterval overrides the configured streaming keep-alive interval.
	// If nil, the configured default is used. If set to <= 0, keep-alives are disabled.
	KeepAliveInterval *time.Duration

	// WriteChunk writes a single data chunk to the response body. It should not flush.
	WriteChunk func(chunk []byte)

	// WriteTerminalError writes an error payload to the response body when streaming fails
	// after headers have already been committed. It should not flush.
	WriteTerminalError func(errMsg *interfaces.ErrorMessage)

	// WriteDone optionally writes a terminal marker when the upstream data channel closes
	// without an error (e.g. OpenAI's `[DONE]`). It should not flush.
	WriteDone func()

	// WriteKeepAlive optionally writes a keep-alive heartbeat. It should not flush.
	// When nil, a standard SSE comment heartbeat is used.
	WriteKeepAlive func()
}

func (h *BaseAPIHandler) ForwardStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, opts StreamForwardOptions) {
	if c == nil {
		return
	}
	if cancel == nil {
		return
	}

	writeChunk := opts.WriteChunk
	if writeChunk == nil {
		writeChunk = func([]byte) {}
	}

	writeKeepAlive := opts.WriteKeepAlive
	if writeKeepAlive == nil {
		writeKeepAlive = func() {
			_, _ = c.Writer.Write([]byte(": keep-alive\n\n"))
		}
	}

	keepAliveInterval := StreamingKeepAliveInterval(h.Cfg)
	if opts.KeepAliveInterval != nil {
		keepAliveInterval = *opts.KeepAliveInterval
	}
	var keepAlive *time.Ticker
	var keepAliveC <-chan time.Time
	if keepAliveInterval > 0 {
		keepAlive = time.NewTicker(keepAliveInterval)
		defer keepAlive.Stop()
		keepAliveC = keepAlive.C
	}

	startedAt := time.Now()
	chunkCount := opts.InitialChunkCount
	var terminalErr *interfaces.ErrorMessage
	for {
		select {
		case <-c.Request.Context().Done():
			err := c.Request.Context().Err()
			logStreamForwardClientDisconnected(c, opts.Model, startedAt, chunkCount, err, "request_context_done")
			cancel(err)
			return
		case chunk, ok := <-data:
			if !ok {
				if err := c.Request.Context().Err(); err != nil {
					logStreamForwardClientDisconnected(c, opts.Model, startedAt, chunkCount, err, "data_closed_after_request_context_done")
					cancel(err)
					return
				}

				// Prefer surfacing a terminal error if one is pending.
				if terminalErr == nil {
					select {
					case errMsg, ok := <-errs:
						if ok && errMsg != nil {
							terminalErr = errMsg
						}
					default:
					}
				}
				if terminalErr != nil {
					if opts.WriteTerminalError != nil {
						opts.WriteTerminalError(terminalErr)
					}
					flusher.Flush()
					cancel(terminalErr.Error)
					return
				}
				if opts.WriteDone != nil {
					opts.WriteDone()
				}
				flusher.Flush()
				cancel(nil)
				return
			}
			writeChunk(chunk)
			chunkCount++
			flusher.Flush()
		case errMsg, ok := <-errs:
			if !ok {
				continue
			}
			if errMsg != nil {
				terminalErr = errMsg
				if opts.WriteTerminalError != nil {
					opts.WriteTerminalError(errMsg)
					flusher.Flush()
				}
			}
			var execErr error
			if errMsg != nil {
				execErr = errMsg.Error
			}
			cancel(execErr)
			return
		case <-keepAliveC:
			writeKeepAlive()
			flusher.Flush()
		}
	}
}

func logStreamForwardClientDisconnected(c *gin.Context, model string, startedAt time.Time, chunkCount int, err error, stage string) {
	log.Infof(
		"stream forward client disconnected request_id=%s model=%s elapsed=%s chunk_count=%d error=%v stage=%s",
		streamForwardRequestID(c),
		model,
		time.Since(startedAt).Truncate(time.Millisecond),
		chunkCount,
		err,
		stage,
	)
}

func streamForwardRequestID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	if requestID := logging.GetGinRequestID(c); requestID != "" {
		return requestID
	}
	if c.Request == nil {
		return ""
	}
	return logging.GetRequestID(c.Request.Context())
}
