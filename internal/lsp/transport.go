package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/logging"
)

const maxLSPMessageBytes = 64 << 20

var (
	ErrTransportClosed = errors.New("LSP transport closed")
	ErrClientClosed    = errors.New("LSP client closed")
)

type callResult struct {
	message *Message
	err     error
}

// Write writes an LSP message to the given writer
func WriteMessage(w io.Writer, msg *Message) error {
	if w == nil {
		return errors.New("LSP writer is required")
	}
	if msg == nil {
		return errors.New("LSP message is required")
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	cnf := config.Get()

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Sending message to server", "method", msg.Method, "id", msg.ID)
	}

	frame := make([]byte, 0, len(data)+64)
	frame = append(frame, "Content-Length: "...)
	frame = strconv.AppendInt(frame, int64(len(data)), 10)
	frame = append(frame, "\r\n\r\n"...)
	frame = append(frame, data...)
	if err = writeAll(w, frame); err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := w.Write(data)
		if written > 0 {
			data = data[written:]
		}
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}

// ReadMessage reads a single LSP message from the given reader
func ReadMessage(r *bufio.Reader) (*Message, error) {
	if r == nil {
		return nil, errors.New("LSP reader is required")
	}
	cnf := config.Get()
	// Read headers
	var contentLength int64
	contentLengthSeen := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read header: %w", err)
		}
		line = strings.TrimSpace(line)

		if cnf != nil && cnf.DebugLSP {
			logging.Debug("Received header", "line", line)
		}

		if line == "" {
			break // End of headers
		}

		key, value, found := strings.Cut(line, ":")
		if found && strings.EqualFold(strings.TrimSpace(key), "Content-Length") {
			parsed, parseErr := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if parseErr != nil || parsed <= 0 {
				return nil, fmt.Errorf("invalid Content-Length %q", strings.TrimSpace(value))
			}
			if parsed > maxLSPMessageBytes {
				return nil, fmt.Errorf("LSP message exceeds %d-byte limit", maxLSPMessageBytes)
			}
			contentLength, contentLengthSeen = parsed, true
		}
	}
	if !contentLengthSeen {
		return nil, errors.New("LSP message is missing Content-Length")
	}

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Content-Length", "length", contentLength)
	}

	// Read content
	content := make([]byte, int(contentLength))
	_, err := io.ReadFull(r, content)
	if err != nil {
		return nil, fmt.Errorf("failed to read content: %w", err)
	}

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Received content", "content", string(content))
	}

	// Parse message
	var msg Message
	if err := json.Unmarshal(content, &msg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal message: %w", err)
	}
	if len(msg.ID) > 0 && string(msg.ID) != "null" {
		var id int32
		if err := json.Unmarshal(msg.ID, &id); err != nil {
			var id string
			if err := json.Unmarshal(msg.ID, &id); err != nil {
				return nil, fmt.Errorf("LSP message ID must be an integer or string")
			}
		}
	}

	return &msg, nil
}

// handleMessages reads and dispatches messages in a loop
func (c *Client) handleMessages() {
	cnf := config.Get()
	for {
		msg, err := ReadMessage(c.stdout)
		if err != nil {
			transportErr := fmt.Errorf("%w: %v", ErrTransportClosed, err)
			c.failPendingCalls(transportErr)
			c.SetServerState(StateError)
			if cnf != nil && cnf.DebugLSP {
				logging.Error("Error reading message", "error", err)
			}
			return
		}

		// Handle server->client request (has both Method and ID)
		if msg.Method != "" && len(msg.ID) > 0 {
			if cnf != nil && cnf.DebugLSP {
				logging.Debug("Received request from server", "method", msg.Method, "id", msg.ID)
			}

			response := &Message{
				JSONRPC: "2.0",
				ID:      msg.ID,
			}

			// Look up handler for this method
			c.serverHandlersMu.RLock()
			handler, ok := c.serverRequestHandlers[msg.Method]
			c.serverHandlersMu.RUnlock()

			if ok {
				result, err := handler(msg.Params)
				if err != nil {
					response.Error = &ResponseError{
						Code:    -32603,
						Message: err.Error(),
					}
				} else {
					rawJSON, err := json.Marshal(result)
					if err != nil {
						response.Error = &ResponseError{
							Code:    -32603,
							Message: fmt.Sprintf("failed to marshal response: %v", err),
						}
					} else {
						response.Result = rawJSON
					}
				}
			} else {
				response.Error = &ResponseError{
					Code:    -32601,
					Message: fmt.Sprintf("method not found: %s", msg.Method),
				}
			}

			// Send response back to server
			if err := c.writeMessage(context.Background(), response); err != nil {
				logging.Error("Error sending response to server", "error", err)
			}

			continue
		}

		// Handle notification (has Method but no ID)
		if msg.Method != "" && len(msg.ID) == 0 {
			c.notificationMu.RLock()
			handler, ok := c.notificationHandlers[msg.Method]
			c.notificationMu.RUnlock()

			if ok {
				if cnf != nil && cnf.DebugLSP {
					logging.Debug("Handling notification", "method", msg.Method)
				}
				go handler(msg.Params)
			} else if cnf != nil && cnf.DebugLSP {
				logging.Debug("No handler for notification", "method", msg.Method)
			}
			continue
		}

		// Handle response to our request (has ID but no Method)
		if len(msg.ID) > 0 && msg.Method == "" {
			if c.deliverResponse(msg) {
				if cnf != nil && cnf.DebugLSP {
					logging.Debug("Received response for request", "id", msg.ID)
				}
			} else if cnf != nil && cnf.DebugLSP {
				logging.Debug("No handler for response", "id", msg.ID)
			}
		}
	}
}

// Call makes a request and waits for the response
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if c == nil {
		return errors.New("LSP client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cnf := config.Get()
	id := c.nextID.Add(1)

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Making call", "method", method, "id", id)
	}

	msg, err := NewRequest(id, method, params)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// Create response channel
	ch := make(chan callResult, 1)
	if err := c.registerCall(id, ch); err != nil {
		return err
	}

	defer func() {
		c.handlersMu.Lock()
		delete(c.handlers, id)
		c.handlersMu.Unlock()
	}()

	// Send request
	if err := c.writeMessage(ctx, msg); err != nil {
		return fmt.Errorf("failed to send request: %w", err)
	}

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Request sent", "method", method, "id", id)
	}

	// Wait for a response, caller cancellation, or transport termination.
	var response callResult
	select {
	case response = <-ch:
	case <-ctx.Done():
		return ctx.Err()
	}
	if response.err != nil {
		return response.err
	}
	resp := response.message
	if resp == nil {
		return fmt.Errorf("%w: empty response", ErrTransportClosed)
	}

	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Received response", "id", id)
	}

	if resp.Error != nil {
		return fmt.Errorf("request failed: %s (code: %d)", resp.Error.Message, resp.Error.Code)
	}

	if result != nil {
		// If result is a json.RawMessage, just copy the raw bytes
		if rawMsg, ok := result.(*json.RawMessage); ok {
			*rawMsg = resp.Result
			return nil
		}
		// Otherwise unmarshal into the provided type
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("failed to unmarshal result: %w", err)
		}
	}

	return nil
}

// Notify sends a notification (a request without an ID that doesn't expect a response)
func (c *Client) Notify(ctx context.Context, method string, params any) error {
	if c == nil {
		return errors.New("LSP client is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cnf := config.Get()
	if cnf != nil && cnf.DebugLSP {
		logging.Debug("Sending notification", "method", method)
	}

	msg, err := NewNotification(method, params)
	if err != nil {
		return fmt.Errorf("failed to create notification: %w", err)
	}

	if err := c.writeMessage(ctx, msg); err != nil {
		return fmt.Errorf("failed to send notification: %w", err)
	}

	return nil
}

func (c *Client) writeMessage(ctx context.Context, msg *Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.acquireWrite(ctx); err != nil {
		return err
	}
	defer c.releaseWrite()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.currentTransportError(); err != nil {
		return err
	}
	if c.stdin == nil {
		err := fmt.Errorf("%w: stdin is unavailable", ErrTransportClosed)
		c.failPendingCalls(err)
		return err
	}
	// A write to an OS pipe can block forever when a broken server stops
	// reading. Cancellation closes the transport to unblock that write. A
	// partially written JSON-RPC frame cannot be reused safely, so failing the
	// whole transport is the deterministic outcome.
	var writing atomic.Bool
	writing.Store(true)
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		if !writing.Swap(false) {
			return
		}
		transportErr := fmt.Errorf("%w: write canceled: %v", ErrTransportClosed, ctx.Err())
		c.failPendingCalls(transportErr)
		_ = c.closeStdin()
		if c.Process != nil {
			_ = c.Process.Kill()
		}
	})
	writeErr := WriteMessage(c.stdin, msg)
	writing.Store(false)
	if !stopCancel() {
		<-cancelDone
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if writeErr != nil {
		transportErr := fmt.Errorf("%w: %v", ErrTransportClosed, writeErr)
		c.failPendingCalls(transportErr)
		return transportErr
	}
	return nil
}

func (c *Client) registerCall(id int32, ch chan callResult) error {
	c.handlersMu.Lock()
	defer c.handlersMu.Unlock()
	if c.transportErr != nil {
		return c.transportErr
	}
	if c.handlers == nil {
		c.handlers = make(map[int32]chan callResult)
	}
	c.handlers[id] = ch
	return nil
}

func (c *Client) deliverResponse(msg *Message) bool {
	if msg == nil || len(msg.ID) == 0 || string(msg.ID) == "null" {
		return false
	}
	// Our outgoing requests use integer IDs. A string containing the same
	// digits is a different JSON-RPC ID and must not complete that request.
	var id int32
	if err := json.Unmarshal(msg.ID, &id); err != nil {
		return false
	}
	c.handlersMu.Lock()
	ch, ok := c.handlers[id]
	if ok {
		delete(c.handlers, id)
	}
	c.handlersMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- callResult{message: msg}:
	default:
	}
	return true
}

func (c *Client) failPendingCalls(err error) {
	if err == nil {
		err = ErrTransportClosed
	}
	c.handlersMu.Lock()
	if c.transportErr != nil {
		c.handlersMu.Unlock()
		return
	}
	c.transportErr = err
	for id, ch := range c.handlers {
		delete(c.handlers, id)
		select {
		case ch <- callResult{err: err}:
		default:
		}
	}
	c.handlersMu.Unlock()
}

func (c *Client) currentTransportError() error {
	c.handlersMu.RLock()
	defer c.handlersMu.RUnlock()
	return c.transportErr
}

type (
	NotificationHandler  func(params json.RawMessage)
	ServerRequestHandler func(params json.RawMessage) (any, error)
)
