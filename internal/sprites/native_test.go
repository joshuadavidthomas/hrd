package sprites

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/muesli/cancelreader"
)

type fakeExitError int

func (e fakeExitError) Error() string { return "provider command failed" }
func (e fakeExitError) ExitCode() int { return int(e) }

type recordedNativeCommand struct {
	name string
	spec nativeCommandSpec
}

type fakeNativeRunner struct {
	mu       sync.Mutex
	commands []recordedNativeCommand
	run      func(context.Context, nativeCommandSpec, int) (string, error)
}

func (r *fakeNativeRunner) Run(ctx context.Context, name string, spec nativeCommandSpec) (string, error) {
	r.mu.Lock()
	index := len(r.commands)
	r.commands = append(r.commands, recordedNativeCommand{name: name, spec: spec})
	r.mu.Unlock()
	if r.run == nil {
		return "", nil
	}
	return r.run(ctx, spec, index)
}

func (r *fakeNativeRunner) specs() []nativeCommandSpec {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]nativeCommandSpec, len(r.commands))
	for i, command := range r.commands {
		result[i] = command.spec
	}
	return result
}

func TestPrepareNativeExistingExecutableSkipsInstall(t *testing.T) {
	runner := &fakeNativeRunner{}
	client := nativePreparationClient(t, runner)
	command, err := client.PrepareNative(context.Background(), "sprite-one")
	if err != nil {
		t.Fatalf("PrepareNative() error = %v", err)
	}
	if command == nil {
		t.Fatal("PrepareNative() returned nil command")
	}
	want := []nativeCommandSpec{
		{path: "/usr/bin/test", args: []string{"-x", nativeExecutable}},
		{path: nativeExecutable, args: []string{"--version"}},
	}
	if got := runner.specs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func TestPrepareNativeMissingExecutableRunsFixedSequence(t *testing.T) {
	runner := &fakeNativeRunner{run: func(_ context.Context, _ nativeCommandSpec, index int) (string, error) {
		switch index {
		case 0:
			return "", fakeExitError(1)
		case 1:
			return "/tmp/herdr-installer\n", nil
		default:
			return "", nil
		}
	}}
	client := nativePreparationClient(t, runner)
	if _, err := client.PrepareNative(context.Background(), "sprite-one"); err != nil {
		t.Fatalf("PrepareNative() error = %v", err)
	}
	want := []nativeCommandSpec{
		{path: "/usr/bin/test", args: []string{"-x", nativeExecutable}},
		{path: "/usr/bin/mktemp", captureOutput: true},
		{path: "/usr/bin/curl", args: []string{"-fsSL", nativeInstallURL, "-o", "/tmp/herdr-installer"}},
		{path: "/bin/sh", args: []string{"/tmp/herdr-installer"}, env: []string{"HOME=/home/sprite"}},
		{path: "/bin/rm", args: []string{"-f", "/tmp/herdr-installer"}},
		{path: nativeExecutable, args: []string{"--version"}},
	}
	if got := runner.specs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func TestPrepareNativeFailuresCleanUpAndStaySanitized(t *testing.T) {
	const token = "secret-native-token"
	for _, failurePath := range []string{"/usr/bin/curl", "/bin/sh", nativeExecutable} {
		failurePath := failurePath
		t.Run(failurePath, func(t *testing.T) {
			runner := &fakeNativeRunner{run: func(_ context.Context, spec nativeCommandSpec, index int) (string, error) {
				if index == 0 {
					return "", fakeExitError(1)
				}
				if index == 1 {
					return "/tmp/install-file\n", nil
				}
				if spec.path == failurePath {
					return "", errors.New("provider failed with " + token)
				}
				return "", nil
			}}
			client := nativePreparationClient(t, runner)
			client.token = token
			_, err := client.PrepareNative(context.Background(), "sprite-one")
			if err == nil {
				t.Fatal("PrepareNative() succeeded, want failure")
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("error leaked token: %v", err)
			}
			specs := runner.specs()
			if failurePath != nativeExecutable {
				foundCleanup := false
				for _, spec := range specs {
					if spec.path == "/bin/rm" {
						foundCleanup = true
					}
				}
				if !foundCleanup {
					t.Fatalf("commands = %#v, want cleanup after failure", specs)
				}
			}
			for _, spec := range specs {
				if strings.Contains(spec.path+strings.Join(spec.args, " ")+strings.Join(spec.env, " "), token) {
					t.Fatalf("command spec leaked token: %#v", spec)
				}
			}
		})
	}
}

func TestPrepareNativeRejectsUnsafeTemporaryPathsWithoutUsingThem(t *testing.T) {
	for _, path := range []string{"relative", "/tmp/a/child", "/var/tmp/file", "/tmp/../tmp/file", "/tmp"} {
		path := path
		t.Run(strings.ReplaceAll(path, "/", "_"), func(t *testing.T) {
			runner := &fakeNativeRunner{run: func(_ context.Context, _ nativeCommandSpec, index int) (string, error) {
				if index == 0 {
					return "", fakeExitError(1)
				}
				return path + "\n", nil
			}}
			_, err := nativePreparationClient(t, runner).PrepareNative(context.Background(), "sprite-one")
			if err == nil {
				t.Fatal("PrepareNative() succeeded for unsafe path")
			}
			if got := len(runner.specs()); got != 2 {
				t.Fatalf("command count = %d, want only test and mktemp", got)
			}
		})
	}
}

func TestPrepareNativeCleansUpAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	runner := &fakeNativeRunner{run: func(commandCtx context.Context, spec nativeCommandSpec, index int) (string, error) {
		switch index {
		case 0:
			return "", fakeExitError(1)
		case 1:
			return "/tmp/install-file\n", nil
		}
		if spec.path == "/usr/bin/curl" {
			cancel()
			return "", commandCtx.Err()
		}
		if spec.path == "/bin/rm" && commandCtx.Err() != nil {
			t.Fatal("cleanup inherited canceled context")
		}
		return "", nil
	}}
	_, err := nativePreparationClient(t, runner).PrepareNative(ctx, "sprite-one")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareNative() error = %v, want context.Canceled", err)
	}
	specs := runner.specs()
	if specs[len(specs)-1].path != "/bin/rm" {
		t.Fatalf("last command = %#v, want cleanup", specs[len(specs)-1])
	}
}

func TestClientCloseWaitsForCanceledNativePreparationCleanup(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	curlStarted := make(chan struct{})
	cleanupStarted := make(chan struct{})
	releaseCleanup := make(chan struct{})
	runner := &fakeNativeRunner{run: func(ctx context.Context, spec nativeCommandSpec, index int) (string, error) {
		switch index {
		case 0:
			return "", fakeExitError(1)
		case 1:
			return "/tmp/install-file\n", nil
		}
		switch spec.path {
		case "/usr/bin/curl":
			close(curlStarted)
			<-ctx.Done()
			return "", ctx.Err()
		case "/bin/rm":
			close(cleanupStarted)
			<-releaseCleanup
			return "", nil
		default:
			return "", nil
		}
	}}
	client := &Client{root: root, token: "token", nativeRunner: runner, nativeDeps: productionNativeDependencies(), closed: make(chan struct{}), bridges: make(map[*bridge]struct{})}
	prepareDone := make(chan error, 1)
	go func() {
		_, err := client.PrepareNative(root, "sprite-one")
		prepareDone <- err
	}()
	<-curlStarted
	cancel()
	closeDone := make(chan struct{})
	go func() {
		_ = client.Close()
		close(closeDone)
	}()
	<-cleanupStarted
	select {
	case <-closeDone:
		t.Fatal("Client.Close returned before installer cleanup")
	default:
	}
	close(releaseCleanup)
	select {
	case <-closeDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Client.Close did not return after cleanup")
	}
	if err := <-prepareDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareNative() error = %v, want context.Canceled", err)
	}
	if _, err := client.PrepareNative(context.Background(), "sprite-one"); !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareNative() after Close error = %v, want context.Canceled", err)
	}
}

func nativePreparationClient(t *testing.T, runner nativeCommandRunner) *Client {
	t.Helper()
	root, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Client{root: root, token: "token", nativeRunner: runner, nativeDeps: productionNativeDependencies()}
}

type fakeTerminal struct {
	mu         sync.Mutex
	cols       int
	rows       int
	raw        int
	restored   int
	restoreErr error
}

func (t *fakeTerminal) IsTerminal(any) bool { return true }
func (t *fakeTerminal) Size(io.Writer) (int, int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cols, t.rows, nil
}
func (t *fakeTerminal) MakeRaw(io.Reader) (any, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.raw++
	return "exact-state", nil
}
func (t *fakeTerminal) Restore(_ io.Reader, state any) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if state != "exact-state" {
		return errors.New("wrong state")
	}
	t.restored++
	return t.restoreErr
}
func (t *fakeTerminal) setSize(cols, rows int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.cols, t.rows = cols, rows
}
func (t *fakeTerminal) restoreCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.restored
}

type fakeCancelReader struct {
	chunks chan []byte
	cancel chan struct{}
	once   sync.Once
}

func newFakeCancelReader(chunks ...[]byte) *fakeCancelReader {
	r := &fakeCancelReader{chunks: make(chan []byte, len(chunks)), cancel: make(chan struct{})}
	for _, chunk := range chunks {
		r.chunks <- chunk
	}
	return r
}
func (r *fakeCancelReader) Read(p []byte) (int, error) {
	select {
	case <-r.cancel:
		return 0, cancelreader.ErrCanceled
	case chunk := <-r.chunks:
		return copy(p, chunk), nil
	}
}
func (r *fakeCancelReader) Cancel() bool { r.once.Do(func() { close(r.cancel) }); return true }
func (r *fakeCancelReader) Close() error { return nil }

type eofCancelReader struct{}

func (eofCancelReader) Read([]byte) (int, error) { return 0, io.EOF }
func (eofCancelReader) Cancel() bool             { return true }
func (eofCancelReader) Close() error             { return nil }

type fakeSignals struct {
	mu      sync.Mutex
	channel chan<- os.Signal
	stops   int
}

func (s *fakeSignals) Notify(ch chan<- os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.channel = ch
}
func (s *fakeSignals) Stop(chan<- os.Signal) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stops++
	s.channel = nil
}
func (s *fakeSignals) trigger() {
	deadline := time.Now().Add(time.Second)
	for {
		s.mu.Lock()
		ch := s.channel
		s.mu.Unlock()
		if ch != nil {
			ch <- os.Interrupt
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(time.Millisecond)
	}
}

type websocketTestServer struct {
	server  *httptest.Server
	baseURL string
}

func newWebSocketTestServer(t *testing.T, handler func(*websocket.Conn, *http.Request)) websocketTestServer {
	t.Helper()
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		handler(conn, r)
	}))
	t.Cleanup(server.Close)
	return websocketTestServer{server: server, baseURL: "ws" + strings.TrimPrefix(server.URL, "http")}
}

func TestNativeCommandExactTransportAndBinaryIO(t *testing.T) {
	const token = "transport-secret"
	serverDone := make(chan struct{})
	server := newWebSocketTestServer(t, func(conn *websocket.Conn, request *http.Request) {
		defer close(serverDone)
		defer func() { _ = conn.Close() }()
		if request.URL.Path != "/v1/sprites/sprite-one/exec" {
			t.Errorf("path = %q", request.URL.Path)
		}
		wantQuery := url.Values{
			"cmd":   {nativeExecutable},
			"path":  {nativeExecutable},
			"tty":   {"true"},
			"stdin": {"true"},
			"env":   {nativeTerminalEnvironment},
			"rows":  {"24"},
			"cols":  {"80"},
		}
		if !reflect.DeepEqual(request.URL.Query(), wantQuery) {
			t.Errorf("query = %#v, want %#v", request.URL.Query(), wantQuery)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read stdin: %v", err)
			return
		}
		if messageType != websocket.BinaryMessage || string(data) != "typed" {
			t.Errorf("stdin frame = (%d, %q)", messageType, data)
		}
		_ = conn.WriteMessage(websocket.BinaryMessage, []byte("remote output"))
		_ = conn.WriteJSON(map[string]any{"type": "exit", "exit_code": 0})
	})
	terminal := &fakeTerminal{cols: 80, rows: 24}
	reader := newFakeCancelReader([]byte("typed"))
	signals := &fakeSignals{}
	var output bytes.Buffer
	command := testNativeCommand(context.Background(), server.baseURL, token, terminal, reader, signals, &output)
	if err := command.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	<-serverDone
	if got := output.String(); got != "remote output" {
		t.Fatalf("output = %q", got)
	}
	assertTerminalRestored(t, terminal, signals)
}

func TestNativeCommandRejectsInvalidTerminalDimensions(t *testing.T) {
	for _, size := range [][2]int{{0, 24}, {80, 0}, {70000, 24}, {80, 70000}} {
		terminal := &fakeTerminal{cols: size[0], rows: size[1]}
		command := testNativeCommand(context.Background(), "ws://example.invalid", "token", terminal, newFakeCancelReader(), &fakeSignals{}, io.Discard)
		err := command.Run()
		if err == nil || !strings.Contains(err.Error(), "read terminal size failed") {
			t.Fatalf("size %v Run() error = %v, want size failure", size, err)
		}
		if terminal.restoreCount() != 0 {
			t.Fatalf("size %v restored terminal without entering raw mode", size)
		}
	}
}

func TestNativeCommandForwardsResize(t *testing.T) {
	connected := make(chan struct{})
	server := newWebSocketTestServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer func() { _ = conn.Close() }()
		close(connected)
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			t.Errorf("read resize: %v", err)
			return
		}
		if messageType != websocket.TextMessage || string(data) != `{"type":"resize","cols":120,"rows":40}` {
			t.Errorf("resize frame = (%d, %s)", messageType, data)
		}
		_ = conn.WriteJSON(map[string]any{"type": "exit", "exit_code": 0})
	})
	terminal := &fakeTerminal{cols: 80, rows: 24}
	signals := &fakeSignals{}
	command := testNativeCommand(context.Background(), server.baseURL, "token", terminal, newFakeCancelReader(), signals, io.Discard)
	errCh := make(chan error, 1)
	go func() { errCh <- command.Run() }()
	<-connected
	terminal.setSize(120, 40)
	signals.trigger()
	if err := <-errCh; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	assertTerminalRestored(t, terminal, signals)
}

func TestNativeCommandExitAndDisconnectContracts(t *testing.T) {
	for _, test := range []struct {
		name     string
		exitCode *int
		wantCode int
	}{
		{name: "explicit zero", exitCode: intPointer(0), wantCode: 0},
		{name: "explicit nonzero", exitCode: intPointer(17), wantCode: 17},
		{name: "disconnect", exitCode: nil, wantCode: -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := newWebSocketTestServer(t, func(conn *websocket.Conn, _ *http.Request) {
				defer func() { _ = conn.Close() }()
				if test.exitCode != nil {
					_ = conn.WriteJSON(map[string]any{"type": "exit", "exit_code": *test.exitCode})
				}
			})
			terminal := &fakeTerminal{cols: 80, rows: 24}
			signals := &fakeSignals{}
			err := testNativeCommand(context.Background(), server.baseURL, "token", terminal, newFakeCancelReader(), signals, io.Discard).Run()
			switch {
			case test.wantCode == 0 && err != nil:
				t.Fatalf("Run() error = %v", err)
			case test.wantCode > 0:
				var exit *NativeExitError
				if !errors.As(err, &exit) || exit.ExitCode() != test.wantCode {
					t.Fatalf("Run() error = %v, want NativeExitError(%d)", err, test.wantCode)
				}
			case test.wantCode < 0 && (err == nil || !strings.Contains(err.Error(), "disconnected")):
				t.Fatalf("Run() error = %v, want disconnect failure", err)
			}
			assertTerminalRestored(t, terminal, signals)
		})
	}
}

func TestNativeCommandCancellationClosesSocketAndReader(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	connected := make(chan struct{})
	serverClosed := make(chan struct{})
	server := newWebSocketTestServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer func() { _ = conn.Close() }()
		close(connected)
		_, _, _ = conn.ReadMessage()
		close(serverClosed)
	})
	terminal := &fakeTerminal{cols: 80, rows: 24}
	reader := newFakeCancelReader()
	signals := &fakeSignals{}
	command := testNativeCommand(root, server.baseURL, "token", terminal, reader, signals, io.Discard)
	errCh := make(chan error, 1)
	go func() { errCh <- command.Run() }()
	<-connected
	cancel()
	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not return after cancellation")
	}
	select {
	case <-serverClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe socket close")
	}
	select {
	case <-reader.cancel:
	default:
		t.Fatal("terminal reader was not canceled")
	}
	assertTerminalRestored(t, terminal, signals)
}

func TestNativeCommandInputEOFClosesSocketAndRestoresTerminal(t *testing.T) {
	serverClosed := make(chan struct{})
	server := newWebSocketTestServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer func() { _ = conn.Close() }()
		_, _, _ = conn.ReadMessage()
		close(serverClosed)
	})
	terminal := &fakeTerminal{cols: 80, rows: 24}
	signals := &fakeSignals{}
	command := testNativeCommand(context.Background(), server.baseURL, "token", terminal, eofCancelReader{}, signals, io.Discard)
	err := command.Run()
	if err == nil || !strings.Contains(err.Error(), "read terminal input failed") {
		t.Fatalf("Run() error = %v, want terminal input failure", err)
	}
	select {
	case <-serverClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not observe socket close after input EOF")
	}
	assertTerminalRestored(t, terminal, signals)
}

func TestNativeCommandReportsTerminalRestoreFailureWithTransportError(t *testing.T) {
	server := newWebSocketTestServer(t, func(conn *websocket.Conn, _ *http.Request) {
		_ = conn.Close()
	})
	terminal := &fakeTerminal{cols: 80, rows: 24, restoreErr: errors.New("restore broke")}
	signals := &fakeSignals{}
	err := testNativeCommand(context.Background(), server.baseURL, "token", terminal, newFakeCancelReader(), signals, io.Discard).Run()
	if err == nil || !strings.Contains(err.Error(), "disconnected") || !strings.Contains(err.Error(), "restore terminal state failed") {
		t.Fatalf("Run() error = %v, want transport and restore failures", err)
	}
	assertTerminalRestored(t, terminal, signals)
}

func TestNativeCommandSanitizesDialErrors(t *testing.T) {
	const token = "never-print-this-token"
	terminal := &fakeTerminal{cols: 80, rows: 24}
	reader := newFakeCancelReader()
	command := &NativeCommand{
		ctx: context.Background(), name: "sprite-one", token: token,
		deps: nativeDependencies{
			baseURL: "wss://example.invalid", dialer: errorDialer{err: errors.New("dial " + token)}, terminal: terminal,
			newCancelReader: func(io.Reader) (nativeCancelReader, error) { return reader, nil }, resizeSignals: &fakeSignals{},
		},
		stdin: bytes.NewReader(nil), stdout: io.Discard, stderr: io.Discard,
	}
	err := command.Run()
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("Run() error = %v, want sanitized failure", err)
	}
	if terminal.restoreCount() != 1 {
		t.Fatalf("terminal restores = %d, want 1", terminal.restoreCount())
	}
}

type errorDialer struct{ err error }

func (d errorDialer) DialContext(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error) {
	return nil, nil, d.err
}

func testNativeCommand(ctx context.Context, baseURL, token string, terminal *fakeTerminal, reader nativeCancelReader, signals *fakeSignals, output io.Writer) *NativeCommand {
	return &NativeCommand{
		ctx: ctx, name: "sprite-one", token: token,
		deps: nativeDependencies{
			baseURL: baseURL, dialer: websocket.DefaultDialer, terminal: terminal,
			newCancelReader: func(io.Reader) (nativeCancelReader, error) { return reader, nil }, resizeSignals: signals,
		},
		stdin: bytes.NewReader(nil), stdout: output, stderr: io.Discard,
	}
}

func assertTerminalRestored(t *testing.T, terminal *fakeTerminal, signals *fakeSignals) {
	t.Helper()
	if got := terminal.restoreCount(); got != 1 {
		t.Fatalf("terminal restores = %d, want 1", got)
	}
	signals.mu.Lock()
	stops := signals.stops
	signals.mu.Unlock()
	if stops != 1 {
		t.Fatalf("signal stops = %d, want 1", stops)
	}
}

func intPointer(value int) *int { return &value }
