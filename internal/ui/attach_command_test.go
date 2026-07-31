package ui

import (
	"bytes"
	"io"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

type fdBuffer struct {
	bytes.Buffer
	fd uintptr
}

func (b *fdBuffer) Fd() uintptr { return b.fd }

type outputExecCommand struct {
	stdout io.Writer
	stderr io.Writer
	run    func(io.Writer, io.Writer) error
}

func (c *outputExecCommand) SetStdin(io.Reader)         {}
func (c *outputExecCommand) SetStdout(output io.Writer) { c.stdout = output }
func (c *outputExecCommand) SetStderr(output io.Writer) { c.stderr = output }
func (c *outputExecCommand) Run() error                 { return c.run(c.stdout, c.stderr) }

func TestAttachmentCommandPreservesOutputFileDescriptor(t *testing.T) {
	command := &outputExecCommand{run: func(io.Writer, io.Writer) error { return nil }}
	wrapped := newAttachmentCommand(command)
	output := &fdBuffer{fd: 42}
	wrapped.SetStdout(output)
	file, ok := command.stdout.(interface{ Fd() uintptr })
	if !ok || file.Fd() != output.fd {
		t.Fatalf("wrapped stdout file descriptor = (%v, %v), want %d", file, ok, output.fd)
	}
}

func TestAttachmentCommandDropsBothStreamsAfterAlternateScreenReset(t *testing.T) {
	reset := ansi.ResetModeAltScreenSaveCursor
	command := &outputExecCommand{run: func(stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, "remote frame"+reset[:3])
		_, _ = io.WriteString(stdout, reset[3:])
		_, _ = io.WriteString(stderr, "run herdr again")
		_, _ = io.WriteString(stdout, "trailing output")
		return nil
	}}
	wrapped := newAttachmentCommand(command)
	var output bytes.Buffer
	wrapped.SetStdout(&output)
	wrapped.SetStderr(io.Discard)
	if err := wrapped.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := output.String(), "remote frame"+reset; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestAttachmentCommandFlushesOutputWithoutAlternateScreenReset(t *testing.T) {
	reset := ansi.ResetModeAltScreenSaveCursor
	command := &outputExecCommand{run: func(stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "plain output"+reset[:len(reset)-1])
		return nil
	}}
	wrapped := newAttachmentCommand(command)
	var output bytes.Buffer
	wrapped.SetStdout(&output)
	wrapped.SetStderr(io.Discard)
	if err := wrapped.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got, want := output.String(), "plain output"+reset[:len(reset)-1]; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}
