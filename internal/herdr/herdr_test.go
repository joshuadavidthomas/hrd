package herdr

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type sessionListRunnerFunc func(context.Context) sessionListResult

func (f sessionListRunnerFunc) run(ctx context.Context) sessionListResult { return f(ctx) }

func TestListSessionsTranslatesDocumentedEnvelope(t *testing.T) {
	t.Parallel()

	const response = `{
		"sessions": [
			{
				"name": "default",
				"default": true,
				"running": true,
				"socket_path": "/tmp/herdr.sock",
				"session_dir": "/home/me/.local/share/herdr/default"
			},
			{
				"name": "alpha",
				"default": false,
				"running": false,
				"socket_path": "/tmp/alpha.sock",
				"session_dir": "/home/me/.local/share/herdr/alpha"
			},
			{
				"name": "zeta",
				"default": false,
				"running": true,
				"socket_path": "/tmp/zeta.sock",
				"session_dir": "/home/me/.local/share/herdr/zeta"
			}
		]
	}`
	client := testClient(sessionListRunnerFunc(func(context.Context) sessionListResult {
		return sessionListResult{stdout: []byte(response)}
	}))

	got, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	want := []Session{
		{Name: "default", Default: true, Running: true},
		{Name: "alpha", Running: false},
		{Name: "zeta", Running: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ListSessions() = %#v, want %#v", got, want)
	}
}

func TestListSessionsAllowsEmptyInventory(t *testing.T) {
	t.Parallel()

	client := testClient(staticRunner(`{"sessions":[]}`))
	got, err := client.ListSessions(context.Background())
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Fatalf("ListSessions() = %#v, want non-nil empty slice", got)
	}
}

func TestListSessionsRejectsMalformedJSONAndInvalidContracts(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"invalid JSON":            `{"sessions":[`,
		"trailing JSON":           `{"sessions":[]} {}`,
		"missing sessions":        `{}`,
		"null sessions":           `{"sessions":null}`,
		"empty name":              `{"sessions":[{"name":"","default":false,"running":false}]}`,
		"missing default":         `{"sessions":[{"name":"work","running":false}]}`,
		"missing running":         `{"sessions":[{"name":"work","default":false}]}`,
		"duplicate name":          `{"sessions":[{"name":"a","default":false,"running":false},{"name":"a","default":false,"running":false}]}`,
		"default not first":       `{"sessions":[{"name":"a","default":false,"running":false},{"name":"default","default":true,"running":false}]}`,
		"wrong default name":      `{"sessions":[{"name":"other","default":true,"running":false}]}`,
		"unmarked default":        `{"sessions":[{"name":"default","default":false,"running":false}]}`,
		"unsorted named sessions": `{"sessions":[{"name":"beta","default":false,"running":false},{"name":"alpha","default":false,"running":false}]}`,
	}

	for name, response := range tests {
		response := response
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			client := testClient(staticRunner(response))
			_, err := client.ListSessions(context.Background())
			_ = assertListErrorKind(t, err, ListErrorMalformed)
		})
	}
}

func TestListSessionsRejectsOversizedOutput(t *testing.T) {
	t.Parallel()

	client := testClient(sessionListRunnerFunc(func(context.Context) sessionListResult {
		return sessionListResult{
			stdout:          []byte(`{"sessions":[]}`),
			stdoutTruncated: true,
		}
	}))
	_, err := client.ListSessions(context.Background())
	listErr := assertListErrorKind(t, err, ListErrorMalformed)
	if !strings.Contains(listErr.Detail, "exceeds") {
		t.Fatalf("oversized output detail = %q, want size guidance", listErr.Detail)
	}
}

func TestListSessionsFailureCategories(t *testing.T) {
	t.Parallel()

	t.Run("missing executable", func(t *testing.T) {
		t.Parallel()
		client := testClient(sessionListRunnerFunc(func(context.Context) sessionListResult {
			return sessionListResult{err: exec.ErrNotFound}
		}))
		_, err := client.ListSessions(context.Background())
		listErr := assertListErrorKind(t, err, ListErrorMissing)
		if !errors.Is(err, exec.ErrNotFound) {
			t.Fatalf("errors.Is(%v, exec.ErrNotFound) = false", err)
		}
		if !strings.Contains(listErr.Error(), "PATH") {
			t.Fatalf("missing error %q does not give PATH guidance", listErr)
		}
	})

	t.Run("nonzero exit with bounded stderr", func(t *testing.T) {
		t.Parallel()
		stderr := strings.Repeat("provider failed\n", 1000)
		failure := errors.New("exit status 7")
		client := testClient(sessionListRunnerFunc(func(context.Context) sessionListResult {
			buffer := newBoundedBuffer(maxStderrBytes - len(truncationMarker))
			_, _ = buffer.Write([]byte(stderr))
			return sessionListResult{
				stderr:          buffer.bytes(),
				stderrTruncated: buffer.truncated,
				err:             failure,
			}
		}))
		_, err := client.ListSessions(context.Background())
		listErr := assertListErrorKind(t, err, ListErrorCommand)
		if !errors.Is(err, failure) {
			t.Fatalf("errors.Is(%v, failure) = false", err)
		}
		if len(listErr.Detail) > maxStderrBytes {
			t.Fatalf("stderr detail length = %d, want <= %d", len(listErr.Detail), maxStderrBytes)
		}
		if !strings.HasSuffix(listErr.Detail, truncationMarker) {
			t.Fatalf("stderr detail %q lacks truncation marker", listErr.Detail)
		}
	})

	t.Run("nonzero exit without stderr", func(t *testing.T) {
		t.Parallel()
		client := testClient(sessionListRunnerFunc(func(context.Context) sessionListResult {
			return sessionListResult{err: errors.New("exit status 9")}
		}))
		_, err := client.ListSessions(context.Background())
		listErr := assertListErrorKind(t, err, ListErrorCommand)
		if listErr.Detail != "exit status 9" {
			t.Fatalf("command detail = %q, want exit status", listErr.Detail)
		}
	})
}

func TestListSessionsAppliesDeadlineAndClassifiesTimeout(t *testing.T) {
	t.Parallel()

	const timeout = 20 * time.Millisecond
	deadlineSeen := make(chan time.Duration, 1)
	client := &Client{
		timeout: timeout,
		runner: sessionListRunnerFunc(func(ctx context.Context) sessionListResult {
			deadline, ok := ctx.Deadline()
			if !ok {
				return sessionListResult{err: errors.New("deadline absent")}
			}
			deadlineSeen <- time.Until(deadline)
			<-ctx.Done()
			return sessionListResult{err: ctx.Err()}
		}),
	}

	_, err := client.ListSessions(context.Background())
	_ = assertListErrorKind(t, err, ListErrorTimeout)
	seen := <-deadlineSeen
	if seen <= 0 || seen > timeout {
		t.Fatalf("runner deadline duration = %v, want within (0, %v]", seen, timeout)
	}
}

func TestListSessionsUsesFiveSecondDeadline(t *testing.T) {
	t.Parallel()

	client := New()
	client.runner = sessionListRunnerFunc(func(ctx context.Context) sessionListResult {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("runner context has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 4*time.Second || remaining > listTimeout {
			t.Fatalf("runner deadline remaining = %v, want about %v", remaining, listTimeout)
		}
		return sessionListResult{stdout: []byte(`{"sessions":[]}`)}
	})
	if _, err := client.ListSessions(context.Background()); err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
}

func TestListSessionsObservesCallerCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	var canceled atomic.Bool
	client := testClient(sessionListRunnerFunc(func(ctx context.Context) sessionListResult {
		close(started)
		<-ctx.Done()
		canceled.Store(true)
		return sessionListResult{err: ctx.Err()}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := client.ListSessions(ctx)
		done <- err
	}()
	<-started
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ListSessions() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ListSessions did not stop after cancellation")
	}
	if !canceled.Load() {
		t.Fatal("runner did not observe cancellation")
	}
}

func TestCommandContextReservesPipeShutdownWithinDeadline(t *testing.T) {
	t.Parallel()

	parentDeadline := time.Now().Add(time.Second)
	ctx, parentCancel := context.WithDeadline(context.Background(), parentDeadline)
	defer parentCancel()
	commandCtx, cancel, waitDelay := commandContext(ctx)
	defer cancel()

	commandDeadline, ok := commandCtx.Deadline()
	if !ok {
		t.Fatal("command context has no deadline")
	}
	if waitDelay <= 0 || commandDeadline.Add(waitDelay).After(parentDeadline) {
		t.Fatalf("command deadline %v plus wait %v exceeds parent %v", commandDeadline, waitDelay, parentDeadline)
	}
}

func TestSessionListCommandIsFixed(t *testing.T) {
	t.Parallel()

	cmd := sessionListCommand(context.Background())
	want := []string{"herdr", "session", "list", "--json"}
	if !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("command args = %#v, want %#v", cmd.Args, want)
	}
}

func TestBoundedBufferAcceptsFullWritesWhileBoundingContent(t *testing.T) {
	t.Parallel()

	buffer := newBoundedBuffer(4)
	if n, err := buffer.Write([]byte("abcdef")); n != 6 || err != nil {
		t.Fatalf("Write() = (%d, %v), want (6, nil)", n, err)
	}
	if got := string(buffer.bytes()); got != "abcd" {
		t.Fatalf("buffer = %q, want %q", got, "abcd")
	}
	if !buffer.truncated {
		t.Fatal("buffer did not record truncation")
	}
}

func TestSafeDetailCannotInjectTerminalControls(t *testing.T) {
	t.Parallel()

	got := safeDetail([]byte("first\n\x1b[31msecond\x00\u202e"), false)
	if got != "first [31msecond" {
		t.Fatalf("safeDetail() = %q, want %q", got, "first [31msecond")
	}
	if strings.ContainsAny(got, "\n\x1b\x00\u202e") {
		t.Fatalf("safeDetail() retained unsafe terminal content: %q", got)
	}
}

func testClient(runner sessionListRunner) *Client {
	return &Client{runner: runner, timeout: time.Second}
}

func staticRunner(response string) sessionListRunner {
	return sessionListRunnerFunc(func(context.Context) sessionListResult {
		return sessionListResult{stdout: []byte(response)}
	})
}

func assertListErrorKind(t *testing.T, err error, want ListErrorKind) *ListError {
	t.Helper()
	var listErr *ListError
	if !errors.As(err, &listErr) {
		t.Fatalf("error = %v (%T), want *ListError", err, err)
	}
	if listErr.Kind != want {
		t.Fatalf("error kind = %v, want %v (error: %v)", listErr.Kind, want, err)
	}
	return listErr
}
