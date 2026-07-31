package ui

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// attachmentCommand keeps output written after Herdr leaves its alternate
// screen from flashing before Bubble Tea redraws hrd. Stdout and stderr share
// one cutoff so a reset on either stream silences both.
type attachmentCommand struct {
	command tea.ExecCommand
	output  *attachmentOutput
}

func newAttachmentCommand(command tea.ExecCommand) tea.ExecCommand {
	return &attachmentCommand{command: command, output: &attachmentOutput{}}
}

func newProcessAttachmentCommand(command *exec.Cmd) tea.ExecCommand {
	return newAttachmentCommand(processExecCommand{Cmd: command})
}

func (c *attachmentCommand) SetStdin(input io.Reader) { c.command.SetStdin(input) }

func (c *attachmentCommand) SetStdout(output io.Writer) {
	c.output.setTarget(output)
	c.command.SetStdout(c.output)
}

func (c *attachmentCommand) SetStderr(io.Writer) { c.command.SetStderr(c.output) }

func (c *attachmentCommand) Run() error {
	runErr := c.command.Run()
	return errors.Join(runErr, c.output.finish())
}

type processExecCommand struct{ *exec.Cmd }

func (c processExecCommand) SetStdin(input io.Reader)   { c.Stdin = input }
func (c processExecCommand) SetStdout(output io.Writer) { c.Stdout = output }
func (c processExecCommand) SetStderr(output io.Writer) { c.Stderr = output }

type attachmentOutput struct {
	mu      sync.Mutex
	target  io.Writer
	pending []byte
	stopped bool
}

func (o *attachmentOutput) setTarget(target io.Writer) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.target = target
}

// Fd preserves terminal detection when the wrapped native command checks the
// writer Bubble Tea assigned to it.
func (o *attachmentOutput) Fd() uintptr {
	o.mu.Lock()
	defer o.mu.Unlock()
	file, ok := o.target.(interface{ Fd() uintptr })
	if !ok {
		return 0
	}
	return file.Fd()
}

func (o *attachmentOutput) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped {
		return len(p), nil
	}

	reset := []byte(ansi.ResetModeAltScreenSaveCursor)
	input := append(append([]byte(nil), o.pending...), p...)
	o.pending = nil
	if index := bytes.Index(input, reset); index >= 0 {
		if err := writeAllOutput(o.target, input[:index+len(reset)]); err != nil {
			return 0, err
		}
		o.stopped = true
		return len(p), nil
	}

	hold := partialSequenceSuffix(input, reset)
	if hold > 0 {
		o.pending = append(o.pending, input[len(input)-hold:]...)
		input = input[:len(input)-hold]
	}
	if err := writeAllOutput(o.target, input); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *attachmentOutput) finish() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.stopped || len(o.pending) == 0 {
		return nil
	}
	err := writeAllOutput(o.target, o.pending)
	o.pending = nil
	return err
}

func partialSequenceSuffix(input, sequence []byte) int {
	limit := min(len(input), len(sequence)-1)
	for length := limit; length > 0; length-- {
		if bytes.Equal(input[len(input)-length:], sequence[:length]) {
			return length
		}
	}
	return 0
}

func writeAllOutput(output io.Writer, data []byte) error {
	if output == nil {
		return errors.New("attachment output is unavailable")
	}
	for len(data) > 0 {
		written, err := output.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
