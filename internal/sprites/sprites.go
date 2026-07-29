// Package sprites translates the Sprites API and owns hrd's fixed SSH setup.
package sprites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptrace"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	sdk "github.com/superfly/sprites-go"
	"golang.org/x/crypto/ssh"

	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

const (
	listTimeout    = 10 * time.Second
	createTimeout  = 30 * time.Second
	prepareTimeout = 3 * time.Minute
)

var namePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type State string

const (
	Running State = "running"
	Warm    State = "warm"
	Cold    State = "cold"
	Unknown State = "unknown"
)

type Sprite struct {
	Name  string
	State State
}
type CreateResult struct {
	Name      string
	Confirmed bool
	Rejected  bool
	Err       error
}

// Client keeps the token in memory and owns every bridge derived from root.
type Client struct {
	sdk     *sdk.Client
	token   string
	root    context.Context
	agent   *sshagent.Manager
	mu      sync.Mutex
	closing bool
	bridges map[*bridge]struct{}
}

func New(root context.Context, token string, agentManager *sshagent.Manager) *Client {
	if token == "" {
		return nil
	}
	if agentManager == nil {
		panic("sprites: nil ssh-agent manager")
	}
	httpClient := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	c := &Client{sdk: sdk.New(token, sdk.WithHTTPClient(httpClient), sdk.WithDisableControl()), token: token, root: root, agent: agentManager, bridges: make(map[*bridge]struct{})}
	go func() { <-root.Done(); c.closeAll() }()
	return c
}

func ValidName(name string) error {
	if !namePattern.MatchString(name) {
		return errors.New("Sprite name must be 1–63 lowercase letters, digits, or interior hyphens")
	}
	return nil
}

func (c *Client) List(ctx context.Context) ([]Sprite, error) {
	if c == nil {
		return nil, errors.New("Sprite access is disabled")
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	var rows []Sprite
	for continuation := ""; ; {
		page, err := c.sdk.ListSprites(ctx, &sdk.ListOptions{MaxResults: 50, ContinuationToken: continuation})
		if err != nil {
			return nil, safeError("list Sprites", err)
		}
		for _, item := range page.Sprites {
			if item.Name == "" {
				return nil, errors.New("Sprite inventory included an unnamed Sprite")
			}
			rows = append(rows, Sprite{Name: item.Name, State: lifecycle(item.Status)})
		}
		// The pinned SDK treats an empty token as the terminal page even when
		// the provider leaves has_more true. Continuing would issue the first
		// page again and duplicate inventory rows.
		if terminalPage(page.HasMore, page.NextContinuationToken) {
			return rows, nil
		}
		continuation = page.NextContinuationToken
	}
}

// Create performs one billable request only. Agent preflight happens before it.
func (c *Client) Create(ctx context.Context, name string) CreateResult {
	if err := ValidName(name); err != nil {
		return CreateResult{Name: name, Rejected: true, Err: err}
	}
	if c == nil {
		return CreateResult{Name: name, Rejected: true, Err: errors.New("Sprite access is disabled")}
	}
	credentials, err := c.agent.Load(ctx)
	if err != nil {
		return CreateResult{Name: name, Rejected: true, Err: err}
	}
	_ = credentials.Close()
	ctx, cancel := context.WithTimeout(ctx, createTimeout)
	defer cancel()
	var wrote bool
	trace := &httptrace.ClientTrace{WroteRequest: func(info httptrace.WroteRequestInfo) { wrote = info.Err == nil }}
	created, err := c.sdk.CreateSprite(httptrace.WithClientTrace(ctx, trace), name, nil)
	if err == nil && created != nil && created.Name() != "" {
		return CreateResult{Name: created.Name(), Confirmed: true}
	}
	var apiError *sdk.APIError
	if wrote && errors.As(err, &apiError) && apiError.StatusCode >= 400 && apiError.StatusCode < 500 && apiError.StatusCode != 408 && apiError.ErrorCode != "" {
		return CreateResult{Name: name, Rejected: true, Err: safeError("create Sprite", err)}
	}
	if !wrote {
		return CreateResult{Name: name, Rejected: true, Err: safeError("create Sprite", err)}
	}
	return CreateResult{Name: name, Err: safeError("Sprite creation may have reached the provider", err)}
}

type Prepared struct {
	Port          int
	Fingerprint   string
	SSHAuthSocket string
	close         func() error
}

func (p Prepared) Close() error {
	if p.close == nil {
		return nil
	}
	return p.close()
}

func (c *Client) Prepare(ctx context.Context, name string) (Prepared, error) {
	if c == nil {
		return Prepared{}, errors.New("Sprite access is disabled")
	}
	if err := ValidName(name); err != nil {
		return Prepared{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, prepareTimeout)
	defer cancel()
	credentials, err := c.agent.Load(ctx)
	if err != nil {
		return Prepared{}, err
	}
	defer func() { _ = credentials.Close() }()
	if err := c.provision(ctx, name, credentials.AuthorizedKeys()); err != nil {
		return Prepared{}, err
	}
	fingerprint, err := c.probe(ctx, name, credentials)
	if err != nil {
		return Prepared{}, err
	}
	prepared, err := c.openBridge(name, fingerprint)
	if err != nil {
		return Prepared{}, err
	}
	prepared.SSHAuthSocket = c.agent.Socket()
	return prepared, nil
}

func (c *Client) provision(ctx context.Context, name, keys string) error {
	run := func(program string, args ...string) error {
		cmd := c.sdk.Sprite(name).CommandContext(ctx, program, args...)
		var stderr bytes.Buffer
		cmd.Stdout = ioDiscard{}
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return safeError("prepare Sprite", err)
		}
		return nil
	}
	if err := run("/bin/true"); err != nil {
		return err
	}
	test := c.sdk.Sprite(name).CommandContext(ctx, "/usr/bin/test", "-x", "/usr/sbin/sshd")
	test.Stdout = ioDiscard{}
	test.Stderr = ioDiscard{}
	if err := test.Run(); err != nil {
		var exit *sdk.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return safeError("check OpenSSH", err)
		}
		if err := run("sudo", "-n", "apt-get", "update"); err != nil {
			return err
		}
		if err := run("sudo", "-n", "env", "DEBIAN_FRONTEND=noninteractive", "apt-get", "install", "-y", "--no-install-recommends", "openssh-server"); err != nil {
			return err
		}
	}
	commands := [][]string{{"sudo", "-n", "install", "-d", "-o", "sprite", "-g", "sprite", "-m", "0700", "/home/sprite/.ssh"}, {"sudo", "-n", "chown", "sprite:sprite", "/home/sprite/.ssh/hrd_authorized_keys"}, {"sudo", "-n", "chmod", "0600", "/home/sprite/.ssh/hrd_authorized_keys"}, {"sudo", "-n", "ssh-keygen", "-A"}, {"sudo", "-n", "install", "-d", "-o", "root", "-g", "root", "-m", "0755", "/run/sshd"}, {"sudo", "-n", "/usr/sbin/sshd", "-t", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "PermitRootLogin=no", "-o", "AllowUsers=sprite", "-o", "AuthorizedKeysFile=.ssh/hrd_authorized_keys"}}
	if err := run(commands[0][0], commands[0][1:]...); err != nil {
		return err
	}
	tee := c.sdk.Sprite(name).CommandContext(ctx, "sudo", "-n", "-u", "sprite", "tee", "/home/sprite/.ssh/hrd_authorized_keys")
	tee.Stdin = strings.NewReader(keys)
	tee.Stdout = ioDiscard{}
	tee.Stderr = ioDiscard{}
	if err := tee.Run(); err != nil {
		return safeError("install Sprite public keys", err)
	}
	for _, command := range commands[1:] {
		if err := run(command[0], command[1:]...); err != nil {
			return err
		}
	}
	stream, err := c.sdk.CreateService(ctx, name, "hrd-sshd", &sdk.ServiceRequest{Cmd: "/usr/sbin/sshd", Args: []string{"-D", "-e", "-o", "PasswordAuthentication=no", "-o", "KbdInteractiveAuthentication=no", "-o", "PermitRootLogin=no", "-o", "AllowUsers=sprite", "-o", "AuthorizedKeysFile=.ssh/hrd_authorized_keys"}})
	if err != nil {
		return safeError("start hrd-sshd", err)
	}
	defer func() { _ = stream.Close() }()
	for {
		event, err := stream.Next()
		if err != nil {
			return safeError("wait for hrd-sshd", err)
		}
		switch event.Type {
		case "started":
			return nil
		case "stdout", "stderr":
			continue
		default:
			return fmt.Errorf("hrd-sshd did not start: %s", event.Type)
		}
	}
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func (c *Client) probe(ctx context.Context, name string, credentials *sshagent.Credentials) (string, error) {
	conn, err := DialProxy(ctx, c.token, name)
	if err != nil {
		return "", safeError("open Sprite SSH tunnel", err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return "", err
	}
	var fingerprint string
	config := &ssh.ClientConfig{User: "sprite", Auth: []ssh.AuthMethod{ssh.PublicKeys(credentials.Signers...)}, HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint = ssh.FingerprintSHA256(key)
		return nil
	}}
	clientConn, channels, requests, err := ssh.NewClientConn(conn, "sprite", config)
	if err != nil {
		return "", safeError("authenticate to Sprite SSH", err)
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()
	_ = conn.SetDeadline(time.Time{})
	if fingerprint == "" {
		return "", errors.New("Sprite SSH did not present a host key")
	}
	return fingerprint, nil
}
func terminalPage(hasMore bool, token string) bool { return !hasMore || token == "" }

// SSHAddCommand starts an hrd-owned agent when needed and returns plain
// ssh-add with the selected socket in its explicit environment.
func (c *Client) SSHAddCommand(ctx context.Context) (*exec.Cmd, error) {
	return c.agent.SSHAddCommand(ctx)
}

func lifecycle(value string) State {
	switch value {
	case "running":
		return Running
	case "warm":
		return Warm
	case "cold":
		return Cold
	default:
		return Unknown
	}
}
func safeError(action string, err error) error {
	if err == nil {
		return fmt.Errorf("%s failed", action)
	}
	text := strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, err.Error())
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return fmt.Errorf("%s: %s", action, text)
}
