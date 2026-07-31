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

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
	"github.com/gorilla/websocket"
	"github.com/muesli/cancelreader"
	sdk "github.com/superfly/sprites-go"

	"github.com/joshuadavidthomas/hrd/internal/herdr"
)

const (
	nativeExecutable          = "/home/sprite/.local/bin/herdr"
	nativeTerminalEnvironment = "TERM=xterm-256color"
	nativeShellEnvironment    = "SHELL=/bin/bash"
	// Herdr publishes no checksum or signature manifest. HTTPS, redirects, the
	// downloaded script, and the asset URLs it selects form the trust boundary.
	nativeInstallURL          = "https://herdr.dev/install.sh"
	nativeBaseURL             = "wss://api.sprites.dev"
	nativeOutputMax           = 4096
	herdrSessionListOutputMax = 1 << 20
	localHerdrConfigMax       = 1 << 20
	herdrExecutableTimeout    = 5 * time.Second
	herdrSessionListTimeout   = 10 * time.Second
	herdrSessionStopTimeout   = 10 * time.Second
	cleanupTimeout            = 5 * time.Second
	nativeInitialResizeDelay  = 10 * time.Millisecond
	nativePingInterval        = 15 * time.Second
	nativeWriteTimeout        = 10 * time.Second
)

type nativeCommandSpec struct {
	path        string
	args        []string
	env         []string
	stdin       []byte
	outputLimit int
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
	if spec.stdin != nil {
		cmd.Stdin = bytes.NewReader(spec.stdin)
	}
	cmd.Stderr = ioDiscard{}
	output := boundedBuffer{limit: spec.outputLimit}
	if spec.outputLimit > 0 {
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
	limit int
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 || len(p) > remaining {
		return 0, errors.New("remote command output exceeded limit")
	}
	return b.Buffer.Write(p)
}

// PrepareHerdr installs or verifies the fixed Herdr executable on a Sprite.
func (c *Client) PrepareHerdr(ctx context.Context, name string) error {
	if c == nil {
		return errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if !c.beginNativePreparation() {
		return context.Canceled
	}
	defer c.nativeWait.Done()
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	var config []byte
	if c.readHerdrConfig != nil {
		var err error
		config, err = c.readHerdrConfig()
		if err != nil {
			return err
		}
	}
	return prepareNativeExecutable(ctx, c.commandRunner(), name, config)
}

// SyncHerdrConfig copies the local Herdr config to an existing Sprite without
// installing or verifying the remote executable.
func (c *Client) SyncHerdrConfig(ctx context.Context, name string) error {
	if c == nil {
		return errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if !c.beginNativePreparation() {
		return context.Canceled
	}
	defer c.nativeWait.Done()
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	if c.readHerdrConfig == nil {
		return nil
	}
	config, err := c.readHerdrConfig()
	if err != nil {
		return err
	}
	return copyHerdrConfig(ctx, c.commandRunner(), name, config)
}

// HerdrInstalled checks only whether the fixed Herdr executable is executable.
func (c *Client) HerdrInstalled(ctx context.Context, name string) (bool, error) {
	if c == nil {
		return false, errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return false, err
	}
	return herdrInstalled(ctx, c.commandRunner(), name)
}

func herdrInstalled(ctx context.Context, runner nativeCommandRunner, name string) (bool, error) {
	commandCtx, cancel := context.WithTimeout(ctx, herdrExecutableTimeout)
	defer cancel()
	_, err := runner.Run(commandCtx, name, nativeCommandSpec{
		path: "/usr/bin/test",
		args: []string{"-x", nativeExecutable},
	})
	if ctxErr := commandCtx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	if err == nil {
		return true, nil
	}
	if commandExitCode(err) == 1 {
		return false, nil
	}
	return false, nativeStepError("check herdr executable", err)
}

// ListHerdrSessions runs Herdr's fixed JSON session inventory command on a Sprite.
func (c *Client) ListHerdrSessions(ctx context.Context, name string) ([]herdr.Session, error) {
	if c == nil {
		return nil, errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}
	commandCtx, cancel := context.WithTimeout(ctx, herdrSessionListTimeout)
	defer cancel()
	output, err := c.commandRunner().Run(commandCtx, name, nativeCommandSpec{
		path:        nativeExecutable,
		args:        []string{"session", "list", "--json"},
		outputLimit: herdrSessionListOutputMax,
	})
	if ctxErr := commandCtx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if err != nil {
		return nil, nativeStepError("list Herdr sessions", err)
	}
	sessions, err := herdr.DecodeSessions([]byte(output))
	if err != nil {
		return nil, errors.New("list Herdr sessions returned invalid JSON")
	}
	return sessions, nil
}

// StopHerdrSession stops one named Herdr session through fixed remote argv.
func (c *Client) StopHerdrSession(ctx context.Context, name, session string) error {
	if c == nil {
		return errors.New("sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return err
	}
	if session == "" {
		return errors.New("herdr session name must not be empty")
	}
	commandCtx, cancel := context.WithTimeout(ctx, herdrSessionStopTimeout)
	defer cancel()
	_, err := c.commandRunner().Run(commandCtx, name, nativeCommandSpec{
		path: nativeExecutable,
		args: []string{"session", "stop", session, "--json"},
	})
	if ctxErr := commandCtx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return nativeStepError("stop Herdr session", err)
	}
	return nil
}

// PrepareNative prepares an interactive Herdr attachment for one Sprite session.
// Installing Herdr is a separate, explicit operation.
func (c *Client) PrepareNative(ctx context.Context, name, session string) (*NativeCommand, error) {
	if c == nil {
		return nil, errors.New("sprite access is disabled")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidName(name); err != nil {
		return nil, err
	}
	if session == "" {
		return nil, errors.New("herdr session name must not be empty")
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
		ctx:     root,
		name:    name,
		session: session,
		token:   c.token,
		deps:    deps,
	}, nil
}

func (c *Client) commandRunner() nativeCommandRunner {
	if c.nativeRunner != nil {
		return c.nativeRunner
	}
	return sdkNativeCommandRunner{client: c.sdk}
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

func prepareNativeExecutable(ctx context.Context, runner nativeCommandRunner, name string, config []byte) error {
	installed, err := herdrInstalled(ctx, runner, name)
	if err != nil {
		return err
	}
	if installed {
		if err := runNativeVersion(ctx, runner, name); err != nil {
			return err
		}
		return copyHerdrConfig(ctx, runner, name, config)
	}

	output, err := runner.Run(ctx, name, nativeCommandSpec{path: "/usr/bin/mktemp", outputLimit: nativeOutputMax})
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
	if err := runNativeVersion(ctx, runner, name); err != nil {
		return err
	}
	return copyHerdrConfig(ctx, runner, name, config)
}

func readLocalHerdrConfig() ([]byte, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return nil, errors.New("find local Herdr config failed")
	}
	configPath := pathpkg.Join(configDir, "herdr", "config.toml")
	info, err := os.Stat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("read local Herdr config failed")
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("local Herdr config is not a regular file")
	}
	if info.Size() > localHerdrConfigMax {
		return nil, fmt.Errorf("local Herdr config exceeds %d bytes", localHerdrConfigMax)
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, errors.New("read local Herdr config failed")
	}
	defer func() { _ = file.Close() }()
	config, err := io.ReadAll(io.LimitReader(file, localHerdrConfigMax+1))
	if err != nil {
		return nil, errors.New("read local Herdr config failed")
	}
	if len(config) > localHerdrConfigMax {
		return nil, fmt.Errorf("local Herdr config exceeds %d bytes", localHerdrConfigMax)
	}
	return config, nil
}

func copyHerdrConfig(ctx context.Context, runner nativeCommandRunner, name string, config []byte) error {
	if config == nil {
		return nil
	}
	const (
		configDir  = "/home/sprite/.config/herdr"
		configPath = configDir + "/config.toml"
	)
	if _, err := runner.Run(ctx, name, nativeCommandSpec{path: "/usr/bin/install", args: []string{"-d", "-m", "0700", configDir}}); err != nil {
		return nativeStepErrorWithContext(ctx, "create Herdr config directory", err)
	}
	output, err := runner.Run(ctx, name, nativeCommandSpec{
		path:        "/usr/bin/mktemp",
		args:        []string{configDir + "/config.toml.tmp.XXXXXX"},
		outputLimit: nativeOutputMax,
	})
	if err != nil {
		return nativeStepErrorWithContext(ctx, "create Herdr config temporary file", err)
	}
	tempPath, err := validHerdrConfigTempPath(output, configDir)
	if err != nil {
		return err
	}

	copyErr := writeHerdrConfig(ctx, runner, name, tempPath, configPath, config)
	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	_, cleanupErr := runner.Run(cleanupCtx, name, nativeCommandSpec{path: "/bin/rm", args: []string{"-f", tempPath}})
	cleanupCancel()
	if copyErr != nil {
		return copyErr
	}
	if cleanupErr != nil {
		return nativeStepError("remove Herdr config temporary file", cleanupErr)
	}
	return nil
}

func writeHerdrConfig(ctx context.Context, runner nativeCommandRunner, name, tempPath, configPath string, config []byte) error {
	steps := []struct {
		action string
		spec   nativeCommandSpec
	}{
		{"write Herdr config", nativeCommandSpec{path: "/usr/bin/tee", args: []string{tempPath}, stdin: config}},
		{"set Herdr config permissions", nativeCommandSpec{path: "/bin/chmod", args: []string{"0600", tempPath}}},
		{"replace Herdr config", nativeCommandSpec{path: "/bin/mv", args: []string{"-fT", tempPath, configPath}}},
	}
	for _, step := range steps {
		if _, err := runner.Run(ctx, name, step.spec); err != nil {
			return nativeStepErrorWithContext(ctx, step.action, err)
		}
	}
	return nil
}

func validHerdrConfigTempPath(output, configDir string) (string, error) {
	path := strings.TrimSpace(output)
	cleaned := pathpkg.Clean(path)
	if path == "" || path != cleaned || pathpkg.Dir(cleaned) != configDir || !strings.HasPrefix(pathpkg.Base(cleaned), "config.toml.tmp.") {
		return "", errors.New("Sprite returned an unsafe Herdr config temporary path")
	}
	return cleaned, nil
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
	return ok && term.IsTerminal(file.Fd())
}

func (nativeSystemTerminal) Size(output io.Writer) (int, int, error) {
	file, ok := output.(fileDescriptor)
	if !ok {
		return 0, 0, errors.New("output has no terminal file descriptor")
	}
	return term.GetSize(file.Fd())
}

func (nativeSystemTerminal) MakeRaw(input io.Reader) (any, error) {
	file, ok := input.(fileDescriptor)
	if !ok {
		return nil, errors.New("input has no terminal file descriptor")
	}
	return term.MakeRaw(file.Fd())
}

func (nativeSystemTerminal) Restore(input io.Reader, state any) error {
	file, fileOK := input.(fileDescriptor)
	terminalState, stateOK := state.(*term.State)
	if !fileOK || !stateOK {
		return errors.New("invalid terminal state")
	}
	return term.Restore(file.Fd(), terminalState)
}

type nativeSystemSignals struct{}

func (nativeSystemSignals) Notify(ch chan<- os.Signal) { signal.Notify(ch, syscall.SIGWINCH) }
func (nativeSystemSignals) Stop(ch chan<- os.Signal)   { signal.Stop(ch) }

// NativeCommand owns the direct Sprite PTY because the SDK does not expose
// the established WebSocket needed to close an interactive command when its
// context is canceled. Its setters match Bubble Tea's ExecCommand interface
// without importing UI code.
type NativeCommand struct {
	ctx     context.Context
	name    string
	session string
	token   string
	deps    nativeDependencies
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
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
	_, _ = io.WriteString(c.stderr, ansi.HideCursor)
	defer func() { _, _ = io.WriteString(c.stderr, ansi.ShowCursor) }()
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

	endpoint, err := buildNativeExecURL(c.deps.baseURL, c.name, c.session, cols, rows)
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

	return c.runPTY(ctx, cancel, ws, reader, cols, rows)
}

func validTerminalSize(cols, rows int) bool {
	return cols > 0 && rows > 0 && cols <= math.MaxUint16 && rows <= math.MaxUint16
}

func buildNativeExecURL(baseURL, name, session string, cols, rows int) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	if session == "" {
		return "", errors.New("herdr session name must not be empty")
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
	query.Add("cmd", "--session")
	query.Add("cmd", session)
	query.Set("path", nativeExecutable)
	query.Set("tty", "true")
	query.Set("stdin", "true")
	query.Add("env", nativeTerminalEnvironment)
	query.Add("env", nativeShellEnvironment)
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

func (c *NativeCommand) runPTY(ctx context.Context, cancel context.CancelFunc, ws *websocket.Conn, reader nativeCancelReader, initialCols, initialRows int) error {
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
	writeResize := func(cols, rows int) error {
		payload, err := json.Marshal(nativeResizeMessage{Type: "resize", Cols: cols, Rows: rows})
		if err != nil {
			return errors.New("encode terminal resize failed")
		}
		if err := write(websocket.TextMessage, payload); err != nil {
			return errors.New("write terminal resize failed")
		}
		return nil
	}

	var workers sync.WaitGroup
	defer func() {
		c.deps.resizeSignals.Stop(resize)
		close(done)
		reader.Cancel()
		_ = ws.Close()
		cancel()
		workers.Wait()
	}()

	// The exec query allocates the PTY, but interactive apps may not paint until
	// the provider delivers a resize control frame. Match the provider SDK's
	// short post-connect delay so the remote PTY is ready to consume it.
	initialResize := time.NewTimer(nativeInitialResizeDelay)
	defer initialResize.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-initialResize.C:
		if err := writeResize(initialCols, initialRows); err != nil {
			return fmt.Errorf("initialize native Sprite terminal: %w", err)
		}
	}

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
				if err := writeResize(cols, rows); err != nil {
					reportNativeWorkerError(workerErr, fmt.Errorf("resize native Sprite terminal: %w", err))
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
