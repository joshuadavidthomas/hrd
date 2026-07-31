package ui

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

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

type connectionState struct {
	phase            connectionPhase
	destination      catalog.Destination
	prepared         catalog.PreparedAttach
	restart          bool
	restartStopped   bool
	transport        catalog.SpriteTransport
	sshRecoveryTried bool
	sshKeySource     string
	sshKeyChoices    []catalog.SSHIdentity
	sshKeyCursor     int
}

func (c connectionState) isSprite() bool {
	_, ok := c.destination.Endpoint.(catalog.SpriteEndpoint)
	return ok
}

func (m Model) updateConnectionKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.connection.phase {
	case connectionChoosingTransport:
		return m.updateTransport(msg)
	case connectionConfirmingStop:
		return m.updateStopConfirmation(msg)
	case connectionConfirmingHostKey:
		if msg.String() == "enter" || msg.String() == "y" {
			return m, m.beginAttach()
		}
		if msg.String() == "esc" || msg.String() == "n" {
			closePrepared(m.connection.prepared)
			m.connection.prepared = nil
			m.connection.restartStopped = false
			return m, m.beginSpriteRefresh()
		}
		return m, nil
	case connectionChoosingSSHKey:
		return m.updateSSHKeyChoice(msg)
	case connectionStopping, connectionPreparing, connectionStartingSSHAgent, connectionAddingSSHKey, connectionAttaching:
		return m, nil
	default:
		panic("ui: invalid Connection phase")
	}
}

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
		m.showReady()
		return m, nil
	case "enter":
		if m.connection.restart {
			cmd := m.beginStop(m.connection.destination)
			return m, tea.Batch(cmd, m.spinner.Tick)
		}
		cmd := m.beginPrepareSprite(m.connection.destination, m.connection.transport)
		if m.connection.transport == catalog.NativePreview {
			return m, cmd
		}
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
			m.showConnection(connectionChoosingTransport)
			return m, nil
		}
		cmd := m.beginStop(m.connection.destination)
		return m, tea.Batch(cmd, m.spinner.Tick)
	case "n", "esc":
		m.connection.restart = false
		m.showReady()
	}
	return m, nil
}

func (m Model) updatePrepareDone(msg prepareDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.connection.phase != connectionPreparing {
		closePrepared(msg.prepared)
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil {
		closePrepared(msg.prepared)
		if m.connection.isSprite() && m.connection.transport == catalog.SSHFallback {
			if source, choices, ok := catalog.SSHKeyChoices(msg.err); ok {
				m.showConnection(connectionChoosingSSHKey)
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
		return m, m.finishConnectionAttempt()
	}
	if msg.prepared == nil {
		m.notice = "Couldn’t prepare destination: no attachment was returned."
		return m, m.finishConnectionAttempt()
	}
	m.connection.prepared = msg.prepared
	switch prepared := msg.prepared.(type) {
	case catalog.PreparedProcessAttach:
		if prepared.Fingerprint != "" {
			m.showConnection(connectionConfirmingHostKey)
			return m, nil
		}
		cmd := m.beginProcessAttach(prepared)
		return m, cmd
	case catalog.PreparedNativeAttach:
		if prepared.Command == nil {
			closePrepared(m.connection.prepared)
			m.connection.prepared = nil
			m.notice = "Couldn’t prepare destination: no native command was returned."
			return m, m.finishConnectionAttempt()
		}
		cmd := m.beginNativeAttach(prepared)
		return m, cmd
	default:
		closePrepared(m.connection.prepared)
		m.connection.prepared = nil
		m.notice = "Couldn’t prepare destination: unknown attachment type."
		return m, m.finishConnectionAttempt()
	}
}

func (m Model) updateAttachDone(msg attachDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.connection.phase != connectionAttaching {
		return m, nil
	}
	closePrepared(m.connection.prepared)
	m.connection.prepared = nil
	if msg.err != nil {
		m.notice = "Attach failed: " + printable(msg.err.Error())
	} else {
		m.notice = ""
	}
	if m.connection.isSprite() {
		m.connection.restartStopped = false
		cmd := m.beginSpriteRefresh()
		return m, cmd
	}
	m.showReady()
	return m, m.beginSilentInventory()
}

func (m Model) updateSSHAddReady(msg sshAddReady) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.connection.phase != connectionStartingSSHAgent {
		return m, nil
	}
	m.cancelOperation()
	if msg.err != nil || msg.command == nil {
		if msg.err == nil {
			msg.err = fmt.Errorf("no ssh-add command")
		}
		m.notice = "Couldn’t start ssh-add: " + printable(msg.err.Error())
		return m, m.finishConnectionAttempt()
	}
	m.showConnection(connectionAddingSSHKey)
	opID := msg.opID
	return m, m.execProcess(msg.command, func(err error) tea.Msg { return sshAddDone{opID: opID, err: err} })
}

func (m Model) updateSSHAddDone(msg sshAddDone) (tea.Model, tea.Cmd) {
	if msg.opID != m.operation.id || m.connection.phase != connectionAddingSSHKey {
		return m, nil
	}
	if msg.err != nil {
		m.notice = "ssh-add did not load a key: " + printable(msg.err.Error())
		return m, m.finishConnectionAttempt()
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
		return m, m.finishConnectionAttempt()
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
		m.showReady()
		return nil
	}
	opID, ctx := m.beginOperation()
	m.showConnection(connectionStopping)
	return func() tea.Msg { return stopDone{opID: opID, err: operations.StopSpriteSession(ctx, selection)} }
}

func (m *Model) beginPrepareLocal(destination catalog.Destination) tea.Cmd {
	opID, ctx := m.beginOperation()
	m.showConnection(connectionPreparing)
	m.connection.destination = destination
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
		return m.finishConnectionAttempt()
	}
	opID, ctx := m.beginOperation()
	m.showConnection(connectionPreparing)
	m.connection.destination = destination
	m.connection.transport = transport
	return func() tea.Msg {
		prepared, err := operations.PrepareSprite(ctx, selection, transport)
		return prepareDone{opID: opID, prepared: prepared, err: err}
	}
}

func (m *Model) beginSSHKeyRecovery() tea.Cmd {
	operations, ok := m.operations.(spriteOperations)
	if !ok {
		m.notice = "Couldn’t start ssh-add."
		return m.finishConnectionAttempt()
	}
	m.connection.sshRecoveryTried = true
	opID, ctx := m.beginOperation()
	m.showConnection(connectionStartingSSHAgent)
	return func() tea.Msg {
		command, err := operations.SSHAddCommand(ctx)
		return sshAddReady{opID: opID, command: command, err: err}
	}
}

func (m *Model) finishConnectionAttempt() tea.Cmd {
	if m.connection.restartStopped {
		m.connection.restartStopped = false
		return m.beginSpriteRefresh()
	}
	m.showReady()
	return nil
}

func (m *Model) retrySpritePrepare() tea.Cmd {
	return m.startPrepareSprite(m.connection.destination, catalog.SSHFallback)
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
	m.showConnection(connectionAttaching)
	opID := m.operation.id
	return m.execProcess(command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) beginNativeAttach(prepared catalog.PreparedNativeAttach) tea.Cmd {
	m.invalidateSpriteAttachObservations()
	m.showConnection(connectionAttaching)
	opID := m.operation.id
	return m.execCommand(prepared.Command, func(err error) tea.Msg { return attachDone{opID: opID, err: err} })
}

func (m *Model) invalidateSpriteAttachObservations() {
	if !m.connection.isSprite() {
		return
	}
	m.home.invalidateSprites()
	m.sessions.invalidate()
	m.syncSpriteItems(m.sessions.selection)
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
	if msg.opID != m.operation.id || m.connection.phase != connectionStopping {
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
		if m.connection.transport == catalog.NativePreview {
			return m, cmd
		}
		return m, tea.Batch(cmd, m.spinner.Tick)
	}
	cmd := m.beginSpriteRefresh()
	return m, cmd
}

func (m Model) connectionView(width, height int) string {
	switch m.connection.phase {
	case connectionConfirmingStop:
		action := "Stop"
		if m.connection.restart {
			action = "Restart"
		}
		return fitView(strings.Join([]string{
			titleStyle.Render(action + " " + printable(m.connection.destination.Label) + "?"),
			warningStyle.Render("Pane processes in this session will exit."),
		}, "\n"), width, height)
	case connectionStopping:
		action := "Stopping "
		if m.connection.restart {
			action = "Stopping before restart: "
		}
		return m.listWithWork(action+printable(m.connection.destination.Label)+"…", width, height)
	case connectionChoosingTransport:
		return fitView(m.transportView(width), width, height)
	case connectionPreparing:
		if m.connection.isSprite() {
			if m.connection.transport == catalog.SSHFallback {
				return m.busyLine("Setting up SSH on "+m.spriteName()+"… Up to three minutes.", width)
			}
			return fitView(m.transportView(width), width, height)
		}
		return m.busyLine("Preparing local session "+printable(m.connection.destination.Label)+"…", width)
	case connectionConfirmingHostKey:
		prepared, _ := m.connection.prepared.(catalog.PreparedProcessAttach)
		return fitView(strings.Join([]string{
			titleStyle.Render("Verify SSH host key"),
			"Authenticated through the Sprites API:",
			accentStyle.Render(printable(prepared.Fingerprint)),
			"Continue only if OpenSSH shows this fingerprint.",
		}, "\n"), width, height)
	case connectionStartingSSHAgent:
		return m.busyLine("Starting a private ssh-agent…", width)
	case connectionAddingSSHKey:
		return fitView("ssh-add has the terminal. Complete its prompt to continue.", width, height)
	case connectionChoosingSSHKey:
		return fitView(m.sshKeyView(width), width, height)
	case connectionAttaching:
		if m.connection.isSprite() && m.connection.transport == catalog.NativePreview {
			return fitView(m.transportView(width), width, height)
		}
		return "Herdr has the terminal."
	default:
		panic("ui: invalid Connection phase")
	}
}

func (m Model) transportView(width int) string {
	type transportOption struct {
		transport catalog.SpriteTransport
		title     string
		badge     string
		detail    string
	}
	options := []transportOption{
		{
			transport: catalog.NativePreview,
			title:     "Native",
			badge:     "recommended",
			detail:    "Herdr runs inside the Sprite. No SSH service or key.",
		},
		{
			transport: catalog.SSHFallback,
			title:     "SSH",
			detail:    "Local Herdr connects through your selected SSH key.",
		},
	}
	chooserWidth := max(12, min(68, width))
	rows := make([]string, 0, len(options)*3)
	for index, option := range options {
		if index > 0 {
			rows = append(rows, borderStyle.Render("├"+strings.Repeat("─", chooserWidth-2)+"┤"))
		}
		rows = append(rows, renderTransportOption(option.title, option.badge, option.detail, option.transport == m.connection.transport, chooserWidth))
	}
	panel := borderStyle.Render("╭"+strings.Repeat("─", chooserWidth-2)+"╮") + "\n" +
		strings.Join(rows, "\n") + "\n" +
		borderStyle.Render("╰"+strings.Repeat("─", chooserWidth-2)+"╯")
	heading := "Choose a connection"
	if m.connection.restart {
		heading = "Choose a connection for the restarted session"
	}
	parts := []string{
		titleStyle.Render(heading),
		mutedStyle.Render("Select where Herdr itself should run."),
		"",
		panel,
	}
	content := strings.Join(parts, "\n")
	return lipgloss.PlaceHorizontal(width, lipgloss.Center, content)
}

func renderTransportOption(title, badge, detail string, selected bool, width int) string {
	contentWidth := max(1, width-4)
	marker := "  "
	markerStyle := lipgloss.NewStyle()
	titleStyleForOption := lipgloss.NewStyle()
	if selected {
		marker = "› "
		markerStyle = accentStyle
		titleStyleForOption = selectedStyle
	}
	badgeStyle := mutedStyle
	detailStyle := mutedStyle

	badgeWidth := lipgloss.Width(badge)
	minimumTitleWidth := min(lipgloss.Width(title), max(1, contentWidth-browserMarkerWidth))
	if badgeWidth > contentWidth-browserMarkerWidth-minimumTitleWidth {
		badge = ""
		badgeWidth = 0
	}
	titleWidth := max(1, contentWidth-browserMarkerWidth-badgeWidth)
	first := markerStyle.Render(marker) + titleStyleForOption.Width(titleWidth).Render(clip(title, titleWidth))
	if badge != "" {
		first += badgeStyle.Render(badge)
	}
	detailWidth := max(1, contentWidth-browserMarkerWidth)
	second := markerStyle.Render(strings.Repeat(" ", browserMarkerWidth)) + detailStyle.Width(detailWidth).Render(clip(detail, detailWidth))
	return borderStyle.Render("│") + " " + first + " " + borderStyle.Render("│") + "\n" +
		borderStyle.Render("│") + " " + second + " " + borderStyle.Render("│")
}

func (m Model) sshKeyView(width int) string {
	source := m.connection.sshKeySource
	if source == "" {
		source = "ssh-agent"
	}
	lines := []string{fmt.Sprintf("%s exposes %d keys. Choose exactly one.", printable(source), len(m.connection.sshKeyChoices))}
	for index, choice := range m.connection.sshKeyChoices {
		selected := index == m.connection.sshKeyCursor
		marker := "  "
		markerStyle := lipgloss.NewStyle()
		labelStyle := lipgloss.NewStyle()
		if selected {
			marker = "› "
			markerStyle = accentStyle
			labelStyle = selectedStyle
		}
		label := printable(choice.Comment)
		if label == "" {
			label = "Unnamed key"
		}
		algorithm := printable(choice.Algorithm)
		algorithmWidth := min(lipgloss.Width(algorithm), max(1, width-browserMarkerWidth-browserStatusGap-1))
		labelWidth := max(1, width-browserMarkerWidth-browserStatusGap-algorithmWidth)
		row := markerStyle.Render(marker) +
			labelStyle.Width(labelWidth).Render(clip(label, labelWidth)) +
			strings.Repeat(" ", browserStatusGap) +
			mutedStyle.Width(algorithmWidth).Render(clip(algorithm, algorithmWidth))
		lines = append(lines, row)
	}
	if m.connection.sshKeyCursor >= 0 && m.connection.sshKeyCursor < len(m.connection.sshKeyChoices) {
		lines = append(lines, mutedStyle.Render("Fingerprint ")+accentStyle.Render(printable(m.connection.sshKeyChoices[m.connection.sshKeyCursor].Fingerprint)))
	}
	return strings.Join(lines, "\n")
}
