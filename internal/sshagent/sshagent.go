// Package sshagent reads public credentials from the current ssh-agent.
package sshagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const timeout = 5 * time.Second

// Credentials owns the agent connection because its signers use it during SSH
// authentication. It contains no private key material.
type Credentials struct {
	PublicKeys []string
	Signers    []ssh.Signer
	connection net.Conn
}

func (c *Credentials) Close() error {
	if c == nil || c.connection == nil {
		return nil
	}
	err := c.connection.Close()
	c.connection = nil
	return err
}

func (c *Credentials) AuthorizedKeys() string {
	if c == nil || len(c.PublicKeys) == 0 {
		return ""
	}
	return strings.Join(c.PublicKeys, "\n") + "\n"
}

// Load validates public keys and retains their agent-backed signers.
func Load(ctx context.Context) (*Credentials, error) {
	socket := os.Getenv("SSH_AUTH_SOCK")
	if socket == "" {
		return nil, errors.New("ssh-agent is not available: SSH_AUTH_SOCK is unset")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to ssh-agent: %w", err)
	}
	client := agent.NewClient(connection)
	keys, err := client.List()
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("read ssh-agent keys: %w", err)
	}
	signers, err := client.Signers()
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("read ssh-agent signers: %w", err)
	}
	if len(keys) == 0 || len(signers) == 0 {
		_ = connection.Close()
		return nil, errors.New("ssh-agent has no public keys")
	}
	lines := make([]string, 0, len(keys))
	for _, identity := range keys {
		key, err := ssh.ParsePublicKey(identity.Blob)
		if err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("decode ssh-agent public key: %w", err)
		}
		lines = append(lines, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key))))
	}
	return &Credentials{PublicKeys: lines, Signers: signers, connection: connection}, nil
}
