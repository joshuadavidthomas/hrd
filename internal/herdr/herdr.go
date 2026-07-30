// Package herdr decodes Herdr's machine-readable session inventory and reads it locally.
package herdr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	listTimeout      = 5 * time.Second
	processWaitDelay = 100 * time.Millisecond
	maxStdoutBytes   = 1 << 20
	maxStderrBytes   = 4096
	truncationMarker = " [truncated]"
)

// Session is the part of a Herdr session record used by the application.
type Session struct {
	Name    string
	Default bool
	Running bool
}

// ListErrorKind identifies failures for which callers can offer different
// recovery steps.
type ListErrorKind uint8

const (
	ListErrorMissing ListErrorKind = iota + 1
	ListErrorTimeout
	ListErrorMalformed
	ListErrorCommand
)

// ListError reports a failure to obtain a complete local session inventory.
type ListError struct {
	Kind   ListErrorKind
	Detail string
	cause  error
}

func (e *ListError) Error() string {
	switch e.Kind {
	case ListErrorMissing:
		return "cannot list local sessions: herdr was not found in PATH"
	case ListErrorTimeout:
		return "cannot list local sessions: herdr session listing timed out after 5 seconds"
	case ListErrorMalformed:
		if e.Detail != "" {
			return "cannot list local sessions: herdr returned malformed session JSON (" + e.Detail + ")"
		}
		return "cannot list local sessions: herdr returned malformed session JSON"
	case ListErrorCommand:
		if e.Detail != "" {
			return "cannot list local sessions: herdr session list failed: " + e.Detail
		}
		return "cannot list local sessions: herdr session list failed"
	default:
		return "cannot list local sessions"
	}
}

// Unwrap retains the original failure for logs and errors.Is checks.
func (e *ListError) Unwrap() error { return e.cause }

// Client lists sessions through the installed herdr executable.
type Client struct {
	runner  sessionListRunner
	timeout time.Duration
}

// New creates a client that runs the fixed `herdr session list --json`
// command with a five-second deadline.
func New() *Client {
	return &Client{
		runner:  execSessionListRunner{},
		timeout: listTimeout,
	}
}

// ListSessions returns a complete, validated local session inventory.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	timeout := c.timeout
	if timeout <= 0 {
		timeout = listTimeout
	}

	listCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	result := c.runner.run(listCtx)
	if result.err != nil {
		if errors.Is(listCtx.Err(), context.Canceled) || errors.Is(result.err, context.Canceled) {
			return nil, context.Canceled
		}
		if errors.Is(listCtx.Err(), context.DeadlineExceeded) || errors.Is(result.err, context.DeadlineExceeded) {
			return nil, &ListError{Kind: ListErrorTimeout, cause: result.err}
		}
		if errors.Is(result.err, exec.ErrNotFound) {
			return nil, &ListError{Kind: ListErrorMissing, cause: result.err}
		}
		detail := safeDetail(result.stderr, result.stderrTruncated)
		if detail == "" {
			detail = safeDetail([]byte(result.err.Error()), false)
		}
		return nil, &ListError{
			Kind:   ListErrorCommand,
			Detail: detail,
			cause:  result.err,
		}
	}

	if result.stdoutTruncated {
		err := fmt.Errorf("session JSON exceeds %d bytes", maxStdoutBytes)
		return nil, &ListError{Kind: ListErrorMalformed, Detail: err.Error(), cause: err}
	}

	sessions, err := DecodeSessions(result.stdout)
	if err != nil {
		return nil, &ListError{
			Kind:   ListErrorMalformed,
			Detail: safeDetail([]byte(err.Error()), false),
			cause:  err,
		}
	}
	return sessions, nil
}

type sessionListResult struct {
	stdout          []byte
	stdoutTruncated bool
	stderr          []byte
	stderrTruncated bool
	err             error
}

type sessionListRunner interface {
	run(context.Context) sessionListResult
}

type execSessionListRunner struct{}

func (execSessionListRunner) run(ctx context.Context) sessionListResult {
	commandCtx, cancel, waitDelay := commandContext(ctx)
	defer cancel()

	cmd := sessionListCommand(commandCtx)
	cmd.WaitDelay = waitDelay
	stdout := newBoundedBuffer(maxStdoutBytes)
	stderr := newBoundedBuffer(maxStderrBytes - len(truncationMarker))
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	if commandCtx.Err() != nil {
		err = commandCtx.Err()
	}
	return sessionListResult{
		stdout:          stdout.bytes(),
		stdoutTruncated: stdout.truncated,
		stderr:          stderr.bytes(),
		stderrTruncated: stderr.truncated,
		err:             err,
	}
}

func commandContext(ctx context.Context) (context.Context, context.CancelFunc, time.Duration) {
	deadline, ok := ctx.Deadline()
	if !ok {
		commandCtx, cancel := context.WithCancel(ctx)
		return commandCtx, cancel, processWaitDelay
	}

	remaining := time.Until(deadline)
	waitDelay := min(processWaitDelay, remaining/4)
	if waitDelay <= 0 {
		commandCtx, cancel := context.WithCancel(ctx)
		return commandCtx, cancel, 0
	}
	commandCtx, cancel := context.WithDeadline(ctx, deadline.Add(-waitDelay))
	return commandCtx, cancel, waitDelay
}

func sessionListCommand(ctx context.Context) *exec.Cmd {
	return exec.CommandContext(ctx, "herdr", "session", "list", "--json")
}

type sessionEnvelope struct {
	Sessions json.RawMessage `json:"sessions"`
}

type wireSession struct {
	Name       string `json:"name"`
	Default    *bool  `json:"default"`
	Running    *bool  `json:"running"`
	SocketPath string `json:"socket_path"`
	SessionDir string `json:"session_dir"`
}

// DecodeSessions decodes and validates Herdr's session list JSON envelope.
// Local and remote transports share this strict wire contract.
func DecodeSessions(data []byte) ([]Session, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var envelope sessionEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("more than one JSON value")
		}
		return nil, fmt.Errorf("decode trailing data: %w", err)
	}
	if len(envelope.Sessions) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Sessions), []byte("null")) {
		return nil, errors.New("missing sessions array")
	}

	var records []wireSession
	if err := json.Unmarshal(envelope.Sessions, &records); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}

	sessions := make([]Session, len(records))
	seenNames := make(map[string]struct{}, len(records))
	seenDefault := false
	previousNamed := ""
	for i, record := range records {
		if record.Name == "" {
			return nil, fmt.Errorf("session %d has an empty name", i)
		}
		if _, exists := seenNames[record.Name]; exists {
			return nil, fmt.Errorf("duplicate session name %q", record.Name)
		}
		seenNames[record.Name] = struct{}{}

		if record.Default == nil {
			return nil, fmt.Errorf("session %q is missing default status", record.Name)
		}
		if record.Running == nil {
			return nil, fmt.Errorf("session %q is missing running status", record.Name)
		}

		if *record.Default {
			if seenDefault {
				return nil, errors.New("more than one session is marked default")
			}
			if i != 0 {
				return nil, errors.New("default session is not first")
			}
			if record.Name != "default" {
				return nil, fmt.Errorf("default session has name %q", record.Name)
			}
			seenDefault = true
		} else {
			if record.Name == "default" {
				return nil, errors.New("session named default is not marked default")
			}
			if previousNamed != "" && previousNamed > record.Name {
				return nil, errors.New("named sessions are not sorted by name")
			}
			previousNamed = record.Name
		}

		sessions[i] = Session{
			Name:    record.Name,
			Default: *record.Default,
			Running: *record.Running,
		}
	}
	return sessions, nil
}

type boundedBuffer struct {
	buf       []byte
	limit     int
	truncated bool
}

func newBoundedBuffer(limit int) *boundedBuffer {
	return &boundedBuffer{buf: make([]byte, 0, limit), limit: limit}
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	available := b.limit - len(b.buf)
	if available > 0 {
		written := min(available, len(p))
		b.buf = append(b.buf, p[:written]...)
	}
	if len(p) > available {
		b.truncated = true
	}
	return len(p), nil
}

func (b *boundedBuffer) bytes() []byte { return bytes.Clone(b.buf) }

func safeDetail(detail []byte, truncated bool) string {
	text := strings.ToValidUTF8(string(detail), "?")
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, text)

	needsMarker := truncated || len(text) > maxStderrBytes
	limit := maxStderrBytes
	if needsMarker {
		limit -= len(truncationMarker)
	}
	if len(text) > limit {
		text = text[:limit]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	if needsMarker {
		text += truncationMarker
	}
	return text
}
