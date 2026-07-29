package sprites

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	pathpkg "path"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/muesli/cancelreader"
	sdk "github.com/superfly/sprites-go"
	"golang.org/x/term"
)

const (
	nativeExecutable          = "/home/sprite/.local/bin/herdr"
	nativeTerminalEnvironment = "TERM=xterm-256color"
	// Herdr publishes no checksum or signature manifest. HTTPS, redirects, the
	// downloaded script, and the asset URLs it selects form the trust boundary.
	nativeInstallURL   = "https://herdr.dev/install.sh"
	nativeBaseURL      = "wss://api.sprites.dev"
	nativeOutputMax    = 4096
	cleanupTimeout     = 5 * time.Second
	nativePingInterval = 15 * time.Second
	nativeWriteTimeout = 10 * time.Second
)

type nativeCommandSpec struct {
	path          string
	args          []string
	env           []string
	captureOutput bool
}

type nativeCommandRunner interface {
	Run(context.Context, string, nativeCommandSpec) (string, error)
}

type sdkNativeCommandRunner struct {
	client *sdk.Client
}

func (r sdkNativeCommandRunner) Run(ctx context.Context, name string, spec nativeCommandSpec) (string, error) {
	cmd := r.client.Sprite(name).CommandContext(ctx, spec.path, spec.args...)
	cmd.Env = spec.env
	cmd.Stderr = ioDiscard{}
	var output boundedBuffer
	if spec.captureOutput {
		cmd.Stdout = &output
	} else {
		cmd.Stdout = ioDiscard{}
	}
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return output.String(), nil
}

type boundedBuffer struct {
	bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := nativeOutputMax - b.Len()
	if remaining <= 0 || len(p) > remaining {
		return 0, errors.New("remote command output exceeded limit")
	}
	return b.Buffer.Write(p)
}

func (c *Client) PrepareNative(ctx context.Context, name string) (*NativeCommand, error) {
	if c == nil {
		return nil, errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}
	if !c.beginNativePreparation() {
		return nil, context.Canceled
	}
	defer c.nativeWait.Done()
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()

	runner := c.nativeRunner
	if runner == nil {
		runner = sdkNativeCommandRunner{client: c.sdk}
	}
	if err := prepareNativeExecutable(ctx, runner, name); err != nil {
		return nil, err
	}

	deps := c.nativeDeps
	if deps.dialer == nil {
		deps = productionNativeDependencies()
	}
	root := c.root
	if root == nil {
		root = context.Background()
	}
	return &NativeCommand{
		ctx:   root,
		name:  name,
		token: c.token,
		deps:  deps,
	}, nil
}

func (c *Client) beginNativePreparation() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return false
	}
	c.nativeWait.Add(1)
	return true
}

func prepareNativeExecutable(ctx context.Context, runner nativeCommandRunner, name string) error {
	check := nativeCommandSpec{path: "/usr/bin/test", args: []string{"-x", nativeExecutable}}
	_, err := runner.Run(ctx, name, check)
	if err == nil {
		return runNativeVersion(ctx, runner, name)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if commandExitCode(err) != 1 {
		return nativeStepError("check herdr executable", err)
	}

	output, err := runner.Run(ctx, name, nativeCommandSpec{path: "/usr/bin/mktemp", captureOutput: true})
	if err != nil {
		return nativeStepErrorWithContext(ctx, "create installer temporary file", err)
	}
	tempPath, err := validInstallerTempPath(output)
	if err != nil {
		return err
	}

	installErr := runNativeInstall(ctx, runner, name, tempPath)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	_, cleanupErr := runner.Run(cleanupCtx, name, nativeCommandSpec{path: "/bin/rm", args: []string{"-f", tempPath}})
	cleanupCancel()
	if installErr != nil {
		return installErr
	}
	if cleanupErr != nil {
		return nativeStepError("remove installer temporary file", cleanupErr)
	}
	return runNativeVersion(ctx, runner, name)
}

func runNativeInstall(ctx context.Context, runner nativeCommandRunner, name, tempPath string) error {
	steps := []struct {
		action string
		spec   nativeCommandSpec
	}{
		{"download herdr installer", nativeCommandSpec{path: "/usr/bin/curl", args: []string{"-fsSL", nativeInstallURL, "-o", tempPath}}},
		{"install herdr", nativeCommandSpec{path: "/bin/sh", args: []string{tempPath}, env: []string{"HOME=/home/sprite"}}},
	}
	for _, step := range steps {
		if _, err := runner.Run(ctx, name, step.spec); err != nil {
			return nativeStepErrorWithContext(ctx, step.action, err)
		}
	}
	return nil
}

func runNativeVersion(ctx context.Context, runner nativeCommandRunner, name string) error {
	_, err := runner.Run(ctx, name, nativeCommandSpec{path: nativeExecutable, args: []string{"--version"}})
	if err != nil {
		return nativeStepErrorWithContext(ctx, "verify herdr executable", err)
	}
	return nil
}

func validInstallerTempPath(output string) (string, error) {
	path := strings.TrimSpace(output)
	cleaned := pathpkg.Clean(path)
	if path == "" || path != cleaned || !pathpkg.IsAbs(cleaned) || pathpkg.Dir(cleaned) != "/tmp" || pathpkg.Base(cleaned) == "." {
		return "", errors.New("Sprite returned an unsafe installer temporary path")
	}
	return cleaned, nil
}

type exitCoder interface {
	ExitCode() int
}

func commandExitCode(err error) int {
	var exit exitCoder
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

func nativeStepErrorWithContext(ctx context.Context, action string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return nativeStepError(action, err)
}

func nativeStepError(action string, err error) error {
	if code := commandExitCode(err); code >= 0 {
		return fmt.Errorf("%s failed with exit code %d", action, code)
	}
	return fmt.Errorf("%s failed", action)
}

// NativeExitError reports the remote herdr process's exit status.
type NativeExitError struct {
	Code int
}

func (e *NativeExitError) Error() string {
	return fmt.Sprintf("herdr exited with status %d", e.Code)
}

func (e *NativeExitError) ExitCode() int { return e.Code }

type nativeWebSocketDialer interface {
	DialContext(context.Context, string, http.Header) (*websocket.Conn, *http.Response, error)
}

type nativeTerminal interface {
	IsTerminal(any) bool
	Size(io.Writer) (cols, rows int, err error)
	MakeRaw(io.Reader) (any, error)
	Restore(io.Reader, any) error
}

type nativeCancelReader interface {
	io.ReadCloser
	Cancel() bool
}

type nativeResizeSignals interface {
	Notify(chan<- os.Signal)
	Stop(chan<- os.Signal)
}

type nativeDependencies struct {
	baseURL         string
	dialer          nativeWebSocketDialer
	terminal        nativeTerminal
	newCancelReader func(io.Reader) (nativeCancelReader, error)
	resizeSignals   nativeResizeSignals
}

func productionNativeDependencies() nativeDependencies {
	return nativeDependencies{
		baseURL: nativeBaseURL,
		dialer: &websocket.Dialer{
			ReadBufferSize:  proxyBufferSize,
			WriteBufferSize: proxyBufferSize,
		},
		terminal: nativeSystemTerminal{},
		newCancelReader: func(reader io.Reader) (nativeCancelReader, error) {
			return cancelreader.NewReader(reader)
		},
		resizeSignals: nativeSystemSignals{},
	}
}

type fileDescriptor interface {
	Fd() uintptr
}

type nativeSystemTerminal struct{}

func (nativeSystemTerminal) IsTerminal(value any) bool {
	file, ok := value.(fileDescriptor)
	return ok && term.IsTerminal(int(file.Fd()))
}

func (nativeSystemTerminal) Size(output io.Writer) (int, int, error) {
	file, ok := output.(fileDescriptor)
	if !ok {
		return 0, 0, errors.New("output has no terminal file descriptor")
	}
	return term.GetSize(int(file.Fd()))
}

func (nativeSystemTerminal) MakeRaw(input io.Reader) (any, error) {
	file, ok := input.(fileDescriptor)
	if !ok {
		return nil, errors.New("input has no terminal file descriptor")
	}
	return term.MakeRaw(int(file.Fd()))
}

func (nativeSystemTerminal) Restore(input io.Reader, state any) error {
	file, fileOK := input.(fileDescriptor)
	terminalState, stateOK := state.(*term.State)
	if !fileOK || !stateOK {
		return errors.New("invalid terminal state")
	}
	return term.Restore(int(file.Fd()), terminalState)
}

type nativeSystemSignals struct{}

func (nativeSystemSignals) Notify(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGWINCH) }
func (nativeSystemSignals) Stop(ch chan<- os.Signal)   { signal.Stop(ch) }

// NativeCommand is an interactive direct PTY command for herdr on a Sprite.
// Its setters match Bubble Tea's ExecCommand interface without importing UI code.
type NativeCommand struct {
	ctx    context.Context
	name   string
	token  string
	deps   nativeDependencies
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (c *NativeCommand) SetStdin(stdin io.Reader)   { c.stdin = stdin }
func (c *NativeCommand) SetStdout(stdout io.Writer) { c.stdout = stdout }
func (c *NativeCommand) SetStderr(stderr io.Writer) { c.stderr = stderr }

func (c *NativeCommand) Run() (runErr error) {
	if c == nil {
		return errors.New("native Sprite command is unavailable")
	}
	if c.stdin == nil || c.stdout == nil || c.stderr == nil {
		return errors.New("native Sprite command requires stdin, stdout, and stderr")
	}
	if !c.deps.terminal.IsTerminal(c.stdin) || !c.deps.terminal.IsTerminal(c.stdout) {
		return errors.New("native Sprite command requires terminal input and output")
	}
	cols, rows, err := c.deps.terminal.Size(c.stdout)
	if err != nil || !validTerminalSize(cols, rows) {
		return errors.New("read terminal size failed")
	}
	state, err := c.deps.terminal.MakeRaw(c.stdin)
	if err != nil {
		return errors.New("enter terminal raw mode failed")
	}
	defer func() {
		if err := c.deps.terminal.Restore(c.stdin, state); err != nil {
			runErr = errors.Join(runErr, errors.New("restore terminal state failed"))
		}
	}()

	reader, err := c.deps.newCancelReader(c.stdin)
	if err != nil {
		return errors.New("create cancelable terminal reader failed")
	}
	defer func() { _ = reader.Close() }()

	endpoint, err := buildNativeExecURL(c.deps.baseURL, c.name, cols, rows)
	if err != nil {
		return err
	}
	header := make(http.Header)
	header.Set("Authorization", "Bearer "+c.token)
	ctx, cancel := context.WithCancel(c.ctx)
	defer cancel()
	ws, response, err := c.deps.dialer.DialContext(ctx, endpoint, header)
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return errors.New("connect native Sprite terminal failed")
	}

	return c.runPTY(ctx, cancel, ws, reader)
}

func validTerminalSize(cols, rows int) bool {
	return cols > 0 && rows > 0 && cols <= math.MaxUint16 && rows <= math.MaxUint16
}

func buildNativeExecURL(baseURL, name string, cols, rows int) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil {
		return "", errors.New("invalid native Sprite WebSocket endpoint")
	}
	u.Path = "/v1/sprites/" + name + "/exec"
	u.RawPath = ""
	u.Fragment = ""
	query := make(url.Values)
	query.Add("cmd", nativeExecutable)
	query.Set("path", nativeExecutable)
	query.Set("tty", "true")
	query.Set("stdin", "true")
	query.Add("env", nativeTerminalEnvironment)
	query.Set("rows", strconv.Itoa(rows))
	query.Set("cols", strconv.Itoa(cols))
	u.RawQuery = query.Encode()
	return u.String(), nil
}

type nativeExitMessage struct {
	Type     string `json:"type"`
	ExitCode *int   `json:"exit_code"`
}

type nativeResizeMessage struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func (c *NativeCommand) runPTY(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, reader nativeCancelReader) error {
	done := make(chan struct{})
	workerErr := make(chan error, 3)
	resize := make(chan os.Signal, 1)
	c.deps.resizeSignals.Notify(resize)

	var writes sync.Mutex
	write := func(messageType int, data []byte) error {
		writes.Lock()
		defer writes.Unlock()
		return ws.WriteMessage(messageType, data)
	}

	var workers sync.WaitGroup
	workers.Add(4)
	go func() {
		defer workers.Done()
		buffer := make([]byte, 32*1024)
		for {
			n, err := reader.Read(buffer)
			if n > 0 {
				data := append([]byte(nil), buffer[:n]...)
				if writeErr := write(websocket.BinaryMessage, data); writeErr != nil {
					reportNativeWorkerError(workerErr, errors.New("write terminal input failed"))
					_ = ws.Close()
					return
				}
			}
			if err != nil {
				if !errors.Is(err, cancelreader.ErrCanceled) && ctx.Err() == nil {
					reportNativeWorkerError(workerErr, errors.New("read terminal input failed"))
					_ = ws.Close()
				}
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		for {
			select {
			case <-done:
				return
			case <-resize:
				cols, rows, err := c.deps.terminal.Size(c.stdout)
				if err != nil || !validTerminalSize(cols, rows) {
					reportNativeWorkerError(workerErr, errors.New("read resized terminal size failed"))
					_ = ws.Close()
					return
				}
				payload, err := json.Marshal(nativeResizeMessage{Type: "resize", Cols: cols, Rows: rows})
				if err != nil {
					reportNativeWorkerError(workerErr, errors.New("encode terminal resize failed"))
					_ = ws.Close()
					return
				}
				if err := write(websocket.TextMessage, payload); err != nil {
					reportNativeWorkerError(workerErr, errors.New("write terminal resize failed"))
					_ = ws.Close()
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		ticker := time.NewTicker(nativePingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				writes.Lock()
				err := ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(nativeWriteTimeout))
				writes.Unlock()
				if err != nil {
					reportNativeWorkerError(workerErr, errors.New("keep native Sprite terminal alive failed"))
					_ = ws.Close()
					return
				}
			}
		}
	}()
	go func() {
		defer workers.Done()
		select {
		case <-ctx.Done():
			reader.Cancel()
			_ = ws.Close()
		case <-done:
		}
	}()

	defer func() {
		c.deps.resizeSignals.Stop(resize)
		close(done)
		reader.Cancel()
		_ = ws.Close()
		cancel()
		workers.Wait()
	}()

	for {
		messageType, data, err := ws.ReadMessage()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			select {
			case err := <-workerErr:
				return err
			default:
			}
			return errors.New("native Sprite terminal disconnected before reporting exit status")
		}
		switch messageType {
		case websocket.BinaryMessage:
			written, writeErr := c.stdout.Write(data)
			if writeErr != nil {
				return errors.New("write native Sprite output failed")
			}
			if written != len(data) {
				return errors.New("write native Sprite output failed: short write")
			}
		case websocket.TextMessage:
			var message nativeExitMessage
			if json.Unmarshal(data, &message) != nil || message.Type != "exit" {
				continue
			}
			if message.ExitCode == nil {
				return errors.New("native Sprite terminal reported an invalid exit status")
			}
			if *message.ExitCode != 0 {
				return &NativeExitError{Code: *message.ExitCode}
			}
			return nil
		}
	}
}

func reportNativeWorkerError(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}
