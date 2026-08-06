package helper

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/tidwall/gjson"

	"github.com/bytedance/gopkg/util/gopool"

	"github.com/gin-gonic/gin"
)

const (
	InitialScannerBufferSize    = 64 << 10  // 64KB (64*1024)
	DefaultMaxScannerBufferSize = 128 << 20 // 64MB (64*1024*1024) default SSE buffer size
	DefaultPingInterval         = 10 * time.Second
	// streamWriteTimeout bounds a single blocked write to a slow client so the
	// unconditional wg.Wait() in cleanup can always finish. Without it, a slow
	// but connected client (full TCP buffer, no server WriteTimeout) could hang
	// the handler forever.
	streamWriteTimeout = 30 * time.Second
	// DefaultMaxPendingStreamBytes bounds the initial SSE prelude held while a
	// caller waits for the first meaningful event before committing a response.
	DefaultMaxPendingStreamBytes = 1 << 20
)

type StreamScannerOptions struct {
	StartResponseWhen             func(data string) bool
	HandleBeforeResponseStartWhen func(data string) bool
	FirstResponseWhen             func(data string) bool
	MaxPendingBytes               int
}

type StreamScannerOutcome struct {
	ResponseStarted    bool
	BufferLimitReached bool
}

// IsValidFirstOutputData reports whether a provider stream event contains a
// usable assistant output, rather than only protocol metadata.
func IsValidFirstOutputData(data string) bool {
	data = strings.TrimSpace(data)
	if data == "" {
		return false
	}
	if !gjson.Valid(data) {
		switch strings.ToLower(data) {
		case "ping", "heartbeat", "keepalive", "[keepalive]", "[done]":
			return false
		default:
			return true
		}
	}
	typeName := gjson.Get(data, "type").String()
	switch typeName {
	case "response.created", "response.queued", "response.in_progress", "response.completed", "response.done", "response.incomplete", "response.failed", "response.cancelled", "response.error", "ping", "heartbeat", "message_start", "message_delta", "content_block_stop", "message_stop":
		return false
	case "content_block_start":
		return hasNonEmptyString(gjson.Get(data, "content_block.text")) ||
			hasNonEmptyString(gjson.Get(data, "content_block.name")) ||
			hasMeaningfulToolCall(gjson.Get(data, "content_block"))
	}

	meaningful := false
	gjson.Get(data, "choices").ForEach(func(_, choice gjson.Result) bool {
		delta := choice.Get("delta")
		if hasNonEmptyString(delta.Get("content")) ||
			hasNonEmptyString(delta.Get("reasoning_content")) ||
			hasNonEmptyString(delta.Get("reasoning")) ||
			hasNonEmptyString(delta.Get("thinking")) ||
			hasNonEmptyString(choice.Get("text")) ||
			hasNonEmptyString(delta.Get("text")) {
			meaningful = true
			return false
		}
		if hasMeaningfulToolCall(delta.Get("tool_calls")) || hasMeaningfulToolCall(delta.Get("function_call")) {
			meaningful = true
			return false
		}
		return true
	})
	if meaningful {
		return true
	}

	gjson.Get(data, "candidates").ForEach(func(_, candidate gjson.Result) bool {
		if hasMeaningfulCandidate(candidate) {
			meaningful = true
			return false
		}
		return true
	})
	if meaningful {
		return true
	}

	for _, path := range []string{
		"delta",
		"delta.text",
		"delta.reasoning",
		"delta.thinking",
		"delta.partial_json",
		"delta.input_json_delta.partial_json",
		"content_block.text",
		"result",
		"answer",
		"output_text",
		"text",
		"completion",
	} {
		if hasNonEmptyString(gjson.Get(data, path)) {
			return true
		}
	}

	// Valid JSON that contains none of the explicit output fields is protocol
	// metadata. In particular, usage-only events and empty tool structures must
	// not stop the first-token timer.
	return false
}

func hasNonEmptyString(value gjson.Result) bool {
	return value.Exists() && value.Type == gjson.String && strings.TrimSpace(value.String()) != ""
}

func hasMeaningfulToolCall(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	meaningful := false
	if value.IsArray() {
		value.ForEach(func(_, item gjson.Result) bool {
			if hasMeaningfulToolCall(item) {
				meaningful = true
				return false
			}
			return true
		})
		return meaningful
	}
	if value.Type != gjson.JSON {
		return false
	}
	function := value.Get("function")
	return hasNonEmptyString(value.Get("name")) ||
		hasNonEmptyString(value.Get("arguments")) ||
		hasNonEmptyJSONValue(value.Get("input")) ||
		hasNonEmptyString(function.Get("name")) ||
		hasNonEmptyString(function.Get("arguments")) ||
		hasNonEmptyJSONValue(function.Get("arguments"))
}

func hasMeaningfulCandidate(candidate gjson.Result) bool {
	parts := candidate.Get("content.parts")
	meaningful := false
	parts.ForEach(func(_, part gjson.Result) bool {
		if hasNonEmptyString(part.Get("text")) ||
			hasNonEmptyString(part.Get("executableCode.code")) ||
			hasNonEmptyString(part.Get("codeExecutionResult.output")) {
			meaningful = true
			return false
		}
		functionCall := part.Get("functionCall")
		if hasNonEmptyString(functionCall.Get("name")) || hasNonEmptyJSONValue(functionCall.Get("args")) {
			meaningful = true
			return false
		}
		return true
	})
	return meaningful
}

func hasNonEmptyJSONValue(value gjson.Result) bool {
	if !value.Exists() {
		return false
	}
	if value.Type == gjson.String {
		return strings.TrimSpace(value.String()) != ""
	}
	if value.Type != gjson.JSON {
		return value.Type == gjson.Number || value.Type == gjson.True || value.Type == gjson.False
	}
	raw := strings.TrimSpace(value.Raw)
	return raw != "" && raw != "null" && raw != "{}" && raw != "[]"
}

func getScannerBufferSize() int {
	if constant.StreamScannerMaxBufferMB > 0 {
		return constant.StreamScannerMaxBufferMB << 20
	}
	return DefaultMaxScannerBufferSize
}

func NewStreamScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, InitialScannerBufferSize), getScannerBufferSize())
	return scanner
}

func copyCodexSSEHeaders(c *gin.Context, resp *http.Response) {
	if c == nil || c.Writer == nil || resp == nil {
		return
	}
	// codex
	for _, name := range []string{"X-Reasoning-Included", "X-Codex-Turn-State"} {
		values := resp.Header.Values(name)
		if !service.ShouldCopyUpstreamHeader(c, name, values) {
			continue
		}
		for _, value := range values {
			if value != "" {
				c.Writer.Header().Add(name, value)
			}
		}
	}
}

// ExtendWriteDeadline pushes the connection write deadline forward before each
// stream write. Best-effort: writers that don't support deadlines (e.g.
// httptest recorders) are silently ignored.
func ExtendWriteDeadline(c *gin.Context) {
	if c == nil || c.Writer == nil {
		return
	}
	_ = http.NewResponseController(c.Writer).SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func StreamScannerHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, dataHandler func(data string, sr *StreamResult)) {
	StreamScannerHandlerWithOptions(c, resp, info, StreamScannerOptions{}, dataHandler)
}

func StreamScannerHandlerWithOptions(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo, options StreamScannerOptions, dataHandler func(data string, sr *StreamResult)) StreamScannerOutcome {

	if resp == nil || dataHandler == nil {
		return StreamScannerOutcome{}
	}

	// 无条件新建 StreamStatus
	info.StreamStatus = relaycommon.NewStreamStatus()

	ctx, cancel := context.WithCancel(c.Request.Context())

	streamingTimeout := time.Duration(constant.StreamingTimeout) * time.Second

	var (
		stopChan           = make(chan bool, 3) // 增加缓冲区避免阻塞
		scanner            = NewStreamScanner(resp.Body)
		ticker             = time.NewTicker(streamingTimeout)
		pingTicker         *time.Ticker
		writeMutex         sync.Mutex     // Mutex to protect concurrent writes
		wg                 sync.WaitGroup // 用于等待所有 goroutine 退出
		cleanupOnce        sync.Once
		stopOnce           sync.Once
		responseStarted    atomic.Bool
		bufferLimitReached bool
	)

	startGateEnabled := options.StartResponseWhen != nil
	maxPendingBytes := options.MaxPendingBytes
	if maxPendingBytes <= 0 {
		maxPendingBytes = DefaultMaxPendingStreamBytes
	}
	startResponse := func() {
		copyCodexSSEHeaders(c, resp)
		SetEventStreamHeaders(c)
		responseStarted.Store(true)
	}
	if !startGateEnabled {
		startResponse()
	}

	stop := func() {
		stopOnce.Do(func() {
			close(stopChan)
		})
	}

	generalSettings := operation_setting.GetGeneralSetting()
	pingEnabled := generalSettings.PingIntervalEnabled && !info.DisablePing
	pingInterval := time.Duration(generalSettings.PingIntervalSeconds) * time.Second
	if pingInterval <= 0 {
		pingInterval = DefaultPingInterval
	}

	if pingEnabled {
		pingTicker = time.NewTicker(pingInterval)
	}

	logger.LogDebug(c, "relay timeout seconds: %d", common.RelayTimeout)
	logger.LogDebug(c, "relay max idle conns: %d", common.RelayMaxIdleConns)
	logger.LogDebug(c, "relay max idle conns per host: %d", common.RelayMaxIdleConnsPerHost)
	logger.LogDebug(c, "streaming timeout seconds: %d", int64(streamingTimeout.Seconds()))
	logger.LogDebug(c, "ping interval seconds: %d", int64(pingInterval.Seconds()))

	cleanup := func() {
		cleanupOnce.Do(func() {
			cancel()
			stop()
			if resp.Body != nil {
				_ = resp.Body.Close()
			}

			ticker.Stop()
			if pingTicker != nil {
				pingTicker.Stop()
			}

			wg.Wait()
		})
	}
	// Ensure gin.Context is not returned to Gin's pool while any stream goroutine can still use it.
	defer cleanup()

	scanner.Split(bufio.ScanLines)

	ctx = context.WithValue(ctx, "stop_chan", stopChan)

	// Handle ping data sending with improved error handling
	if pingEnabled && pingTicker != nil {
		wg.Add(1)
		gopool.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					logger.LogError(c, fmt.Sprintf("ping goroutine panic: %v", r))
					info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("ping panic: %v", r))
					stop()
				}
				logger.LogDebug(c, "ping goroutine exited")
				wg.Done()
			}()

			// 添加超时保护，防止 goroutine 无限运行
			maxPingDuration := 30 * time.Minute // 最大 ping 持续时间
			pingTimeout := time.NewTimer(maxPingDuration)
			defer pingTimeout.Stop()

			for {
				select {
				case <-pingTicker.C:
					if !responseStarted.Load() {
						continue
					}
					var err error
					func() {
						writeMutex.Lock()
						defer writeMutex.Unlock()
						ExtendWriteDeadline(c)
						err = PingData(c)
					}()
					if err != nil {
						logger.LogError(c, "ping data error: "+err.Error())
						info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPingFail, err)
						return
					}
					logger.LogDebug(c, "ping data sent")
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				case <-c.Request.Context().Done():
					// 监听客户端断开连接
					return
				case <-pingTimeout.C:
					logger.LogError(c, "ping goroutine max duration reached")
					return
				}
			}
		})
	}

	dataChan := make(chan string, 10)

	wg.Add(1)
	gopool.Go(func() {
		defer func() {
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("data handler goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("handler panic: %v", r))
			}
			stop()
			wg.Done()
		}()
		sr := newStreamResult(info.StreamStatus, &responseStarted)
		for data := range dataChan {
			sr.reset()
			func() {
				writeMutex.Lock()
				defer writeMutex.Unlock()
				ExtendWriteDeadline(c)
				dataHandler(data, sr)
			}()
			if sr.IsStopped() {
				return
			}
		}
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonEOF, nil)
	})

	// Scanner goroutine with improved error handling
	wg.Add(1)
	common.RelayCtxGo(ctx, func() {
		defer func() {
			close(dataChan)
			if r := recover(); r != nil {
				logger.LogError(c, fmt.Sprintf("scanner goroutine panic: %v", r))
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonPanic, fmt.Errorf("scanner panic: %v", r))
			}
			logger.LogDebug(c, "scanner goroutine exited")
			wg.Done()
		}()

		var pendingData []string
		pendingBytes := 0
		for scanner.Scan() {
			// 检查是否需要停止
			select {
			case <-stopChan:
				return
			case <-ctx.Done():
				return
			default:
			}

			ticker.Reset(streamingTimeout)
			data := strings.TrimSpace(scanner.Text())
			logger.LogDebug(c, "stream scanner data: %s", data)

			if data == "[DONE]" {
				continue
			}
			if !strings.HasPrefix(data, "data:") {
				continue
			}
			data = data[5:]
			data = strings.TrimSpace(data)
			if data == "" {
				continue
			}
			if !strings.HasPrefix(data, "[DONE]") {
				info.ReceivedResponseCount++
				firstResponseWhen := options.FirstResponseWhen
				if firstResponseWhen == nil {
					firstResponseWhen = IsValidFirstOutputData
				}
				if firstResponseWhen(data) {
					info.SetFirstResponseTime()
				}
				if startGateEnabled && !responseStarted.Load() {
					shouldStart := options.StartResponseWhen(data)
					shouldHandleBeforeStart := options.HandleBeforeResponseStartWhen != nil && options.HandleBeforeResponseStartWhen(data)
					if shouldHandleBeforeStart && !shouldStart {
						select {
						case dataChan <- data:
						case <-ctx.Done():
							return
						case <-stopChan:
							return
						}
						continue
					}
					wouldExceedLimit := len(data) > maxPendingBytes-pendingBytes
					if !shouldStart && !wouldExceedLimit {
						pendingData = append(pendingData, data)
						pendingBytes += len(data)
						continue
					}
					if !shouldStart {
						bufferLimitReached = true
						logger.LogWarn(c, fmt.Sprintf("stream prelude exceeded %d bytes; starting downstream response", maxPendingBytes))
					}
					startResponse()
					for _, pending := range pendingData {
						select {
						case dataChan <- pending:
						case <-ctx.Done():
							return
						case <-stopChan:
							return
						}
					}
					pendingData = nil
					pendingBytes = 0
				}

				select {
				case dataChan <- data:
				case <-ctx.Done():
					return
				case <-stopChan:
					return
				}
			} else {
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonDone, nil)
				logger.LogDebug(c, "received [DONE], stopping scanner")
				return
			}
		}

		if err := scanner.Err(); err != nil {
			if err != io.EOF {
				logger.LogError(c, "scanner error: "+err.Error())
				info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonScannerErr, err)
			}
		}
	})

	// 主循环等待完成或超时
	select {
	case <-ticker.C:
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonTimeout, nil)
	case <-stopChan:
		// EndReason already set by the goroutine that triggered stopChan
	case <-c.Request.Context().Done():
		// 客户端断开：立即 cleanup 关闭上游 resp.Body，解除 scanner 阻塞并让上游停止生成，
		// 避免为已放弃的请求继续消费上游 token。
		info.StreamStatus.SetEndReason(relaycommon.StreamEndReasonClientGone, c.Request.Context().Err())
	}

	cleanup()
	if info.StreamStatus.IsNormalEnd() && !info.StreamStatus.HasErrors() {
		logger.LogInfo(c, fmt.Sprintf("stream ended: %s", info.StreamStatus.Summary()))
	} else {
		logger.LogError(c, fmt.Sprintf("stream ended: %s, received=%d", info.StreamStatus.Summary(), info.ReceivedResponseCount))
	}
	return StreamScannerOutcome{
		ResponseStarted:    responseStarted.Load(),
		BufferLimitReached: bufferLimitReached,
	}
}
