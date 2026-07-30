package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
)

type prepareDone struct {
	opID     uint64
	prepared catalog.PreparedAttach
	err      error
}

type stopDone struct {
	opID uint64
	err  error
}

type attachDone struct {
	opID uint64
	err  error
}

type sshAddReady struct {
	opID    uint64
	command *exec.Cmd
	err     error
}

type sshAddDone struct {
	opID uint64
	err  error
}

type prepareOperation struct {
	destination catalog.Destination
	transport   catalog.SpriteTransport
	sprite      bool
}

type connectionState struct {
	destination      catalog.Destination
	prepare          prepareOperation
	prepared         catalog.PreparedAttach
	restart          bool
	restartStopped   bool
	transport        catalog.SpriteTransport
	sshRecoveryTried bool
	sshKeySource     string
	sshKeyChoices    []catalog.SSHIdentity
	sshKeyCursor     int
}

// Model is hrd's Bubble Tea model.
func (m Model) updateTransport(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "down", "j", "k":
		if m.connection.transport == catalog.NativePreview {
			m.connection.transport = catalog.SSHFallback
		} else {
			m.connection.transport = catalog.NativePreview
		}
		return m, nil
	case "esc":
		m.connection.restart = false
		m.phase = phaseReady
		return m, nil
	case "enter":
		if m.connection.restart {
			cmd := m.beginStop(m.connection.destination)
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		cmd := m.beginPrepareSprite(m.connection.destination, m.connection.transport)
		return m, tea.Batch(cmd, m.spinner.Tick)
	default:
		return m, nil
	}
}

func (m Model) updateStopConfirmation(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		if m.connection.restart {
			m.connection.transport = catalog.NativePreview
			m.phase = phaseChoosingSpriteTransport
			return m, nil
		}
		cmd := m.beginStop(m.connection.destination)
		return m, tea.Batch(cmd, m.spinner.Tick)
	case "n", "esc":
		m.connection.restart = false
		m.phase = phaseReady
	}
	return m, nil
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.phase != phasePreparing {
		closePrepared(msg.prepared)
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		closePrepared(msg.prepared)
		if !m.connection.restartStopped && m.connection.prepare.sprite && m.connection.prepare.transport == catalog.SSHFallback {
			if source, choices, ok := catalog.SSHKeyChoices(msg.err); ok {
				m.phase = phaseChoosingSSHKey
				m.connection.sshKeySource = printable(source)
				m.connection.sshKeyChoices = append([]catalog.SSHIdentity(nil), choices...)
				m.connection.sshKeyCursor = 0
				return m, nil
			}
			if catalog.NeedsSSHKey(msg.err) && !m.connection.sshRecoveryTried {
				cmd := m.beginSSHKeyRecovery()
				return m, cmd
			}
		}
		m.notice = prepareFailureNotice(msg.err)
		if m.connection.restartStopped {
			m.connection.restartStopped = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
	if msg.prepared == nil {
		m.notice = "Couldn’t prepare destination: no attachment was returned."
		if m.connection.restartStopped {
			m.connection.restartStopped = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
	m.connection.prepared = msg.prepared
	switch prepared := msg.prepared.(type) {
	case catalog.PreparedProcessAttach:
		if prepared.Fingerprint != "" {
			m.phase = phaseConfirmingHostKey
			return m, nil
		}
		cmd := m.beginProcessAttach(prepared)
		return m, cmd
	case catalog.PreparedNativeAttach:
		if prepared.Command == nil {
			closePrepared(m.connection.prepared)
			m.connection.prepared = nil
			m.notice = "Couldn’t prepare destination: no native command was returned."
			if m.connection.restartStopped {
				m.connection.restartStopped = false
				cmd := m.beginSpriteRefresh()
				return m, cmd
			}
			m.phase = phaseReady
			return m, nil
		}
		cmd := m.beginNativeAttach(prepared)
		return m, cmd
	default:
		closePrepared(m.connection.prepared)
		m.connection.prepared = nil
		m.notice = "Couldn’t prepare destination: unknown attachment type."
		if m.connection.restartStopped {
			m.connection.restartStopped = false
			cmd := m.beginSpriteRefresh()
			return m, cmd
		}
		m.phase = phaseReady
		return m, nil
	}
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.phase != phaseAttaching {
		return m, nil
	}
	closePrepared(m.connection.prepared)
	m.connection.prepared = nil
	if msg.err != nil {
		m.notice = "Attach failed: " + printable(msg.err.Error())
	} else {
		m.notice = ""
	}
	if m.connection.prepare.sprite {
		m.connection.restartStopped = false
		cmd := m.beginSpriteRefresh()
		return m, cmd
	}
	m.phase = phaseReady
	return m, m.beginSilentInventory()
}

func (m Model) updateSSHAddReady(msg sshAddReady) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.phase != phaseStartingSSHAgent {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil || msg.command == nil {
		if msg.err == nil {
			msg.err = fmt.Errorf("no ssh-add command")
		}
		m.notice = "Couldn’t start ssh-add: " + printable(msg.err.Error())
		m.phase = phaseReady
		return m, nil
	}
	m.phase = phaseAddingSSHKey
	opID := msg.opID
	return m, m.execProcess(msg.command, func(err error) tea.Msg { return sshAddDone{opID: opID, err: err} })
}

func (m Model) updateSSHAddDone(msg sshAddDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.phase != phaseAddingSSHKey {
		return m, nil
	}
	if msg.err != nil {
		m.notice = "ssh-add did not load a key: " + printable(msg.err.Error())
		m.phase = phaseReady
		return m, nil
	}
	cmd := m.retrySpritePrepare()
	return m, tea.Batch(cmd, m.spinner.Tick)
}

func (m Model) updateSSHKeyChoice(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		m.connection.sshKeyCursor = max(0, m.connection.sshKeyCursor-1)
	case "down", "j":
		m.connection.sshKeyCursor = min(max(0, len(m.connection.sshKeyChoices)-1), m.connection.sshKeyCursor+1)
	case "esc":
		m.connection.sshKeyChoices = nil
		m.phase = phaseReady
	case "enter":
		if m.connection.sshKeyCursor < 0 || m.connection.sshKeyCursor >= len(m.connection.sshKeyChoices) {
			return m, nil
		}
		operations, ok := m.operations.(spriteOperations)
		if !ok {
			m.notice = "Couldn’t select an SSH key."
			return m, nil
		}
		if err := operations.SelectSSHKey(m.connection.sshKeyChoices[m.connection.sshKeyCursor].Fingerprint); err != nil {
			m.notice = "Couldn’t select SSH key: " + printable(err.Error())
			return m, nil
		}
		m.connection.sshKeyChoices = nil
		cmd := m.retrySpritePrepare()
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	return m, nil
}

func (m *Model) beginStop(destination catalog.Destination) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	selection, err := catalog.SelectSpriteSession(destination)
	if err != nil {
		m.notice = "Couldn’t stop session: " + printable(err.Error())
		m.phase = phaseReady
		return nil
	}
	opID, ctx := m.beginOperation()
	m.phase = phaseStopping
	return func() tea.Msg { return stopDone{opID: opID, err: operations.StopSpriteSession(ctx, selection)} }
}

func (m *Model) beginPrepareLocal(destination catalog.Destination) tea.Cmd {
	opID, ctx := m.beginOperation()
	m.phase = phasePreparing
	m.connection.prepare = prepareOperation{destination: destination}
	operations := m.operations
	return func() tea.Msg {
		prepared, err := operations.Prepare(ctx, destination)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginPrepareSprite(destination catalog.Destination, transport catalog.SpriteTransport) tea.Cmd {
	m.connection.sshRecoveryTried = false
	return m.startPrepareSprite(destination, transport)
}

func (m *Model) startPrepareSprite(destination catalog.Destination, transport catalog.SpriteTransport) tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		return nil
	}
	selection, err := catalog.SelectSpriteSession(destination)
	if err != nil {
		m.notice = prepareFailureNotice(err)
		m.phase = phaseReady
		return nil
	}
	opID, ctx := m.beginOperation()
	m.phase = phasePreparing
	m.connection.prepare = prepareOperation{destination: destination, transport: transport, sprite: true}
	return func() tea.Msg {
		prepared, err := operations.PrepareSprite(ctx, selection, transport)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginSSHKeyRecovery() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		m.notice = "Couldn’t start ssh-add."
		m.phase = phaseReady
		return nil
	}
	m.connection.sshRecoveryTried = true
	opID, ctx := m.beginOperation()
	m.phase = phaseStartingSSHAgent
	return func() tea.Msg {
		command, err := operations.SSHAddCommand(ctx)
		return sshAddReady{opID: opID, command: command, err: err}
	}
}

func (m *Model) retrySpritePrepare() tea.Cmd {
	return m.startPrepareSprite(m.connection.prepare.destination, catalog.SSHFallback)
}

func (m *Model) beginAttach() tea.Cmd {
	prepared, ok := m.connection.prepared.(catalog.PreparedProcessAttach)
	if !ok {
		return nil
	}
	return m.beginProcessAttach(prepared)
}

func (m *Model) beginProcessAttach(prepared catalog.PreparedProcessAttach) tea.Cmd {
	m.invalidateSpriteAttachObservations()
	command := exec.CommandContext(m.root, "herdr", prepared.HerdrArgs()...)
	environment := withoutEnvironment(os.Environ(), "SPRITE_TOKEN")
	if socket := prepared.SSHAuthSocket(); socket != "" {
		environment = withEnvironment(environment, "SSH_AUTH_SOCK", socket)
	}
	command.Env = environment
	m.phase = phaseAttaching
	opID := m.operation.id
	return m.execProcess(command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginNativeAttach(prepared catalog.PreparedNativeAttach) tea.Cmd {
	m.invalidateSpriteAttachObservations()
	m.phase = phaseAttaching
	opID := m.operation.id
	return m.execCommand(prepared.Command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) invalidateSpriteAttachObservations() {
	if !m.connection.prepare.sprite {
		return
	}
	m.home.invalidateSprites()
	m.sessions.invalidate()
	m.syncSpriteItems(m.selectedID)
}

func closePrepared(prepared catalog.PreparedAttach) {
	if prepared != nil {
		_ = prepared.Close()
	}
}

func prepareFailureNotice(err error) string {
	if err == nil {
		return "Couldn’t prepare destination."
	}
	return "Couldn’t prepare destination: " + printable(err.Error())
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return result
}

func withEnvironment(environment []string, name, value string) []string {
	result := withoutEnvironment(environment, name)
	return append(result, name+"="+value)
}
func (m Model) updateStopDone(msg stopDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.phase != phaseStopping {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		m.connection.restart = false
		m.notice = "Couldn’t stop session: " + printable(msg.err.Error())
		m.sessions.invalidate()
		m.syncSpriteItems(m.connection.destination.ID)
		cmd := m.beginSpriteRefresh()
		return m, cmd
	}
	m.sessions.invalidate()
	m.syncSpriteItems(m.connection.destination.ID)
	if m.connection.restart {
		m.connection.restart = false
		m.connection.restartStopped = true
		cmd := m.beginPrepareSprite(m.connection.destination, m.connection.transport)
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	cmd := m.beginSpriteRefresh()
	return m, cmd
}
