package ui

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

func TestNamedSpriteNativeAndSSHPreparation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport catalog.SpriteTransport
		move      bool
	}{
		{"native", catalog.NativePreview, false},
		{"ssh", catalog.SSHFallback, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			terminal := &fakeTerminalCommand{}
			ops.prepareSpriteFn = func(_ context.Context, selection catalog.SpriteSessionSelection, transport catalog.SpriteTransport, _ int) (catalog.PreparedAttach, error) {
				if want := spriteSessionSelection("devbox", "work"); !reflect.DeepEqual(selection, want) || transport != tc.transport {
					t.Fatalf("PrepareSprite selection/transport = %#v/%v", selection, transport)
				}
				if transport == catalog.NativePreview {
					return catalog.PreparedNativeAttach{Command: terminal}, nil
				}
				return catalog.PrepareSpriteSSH("devbox", "work", sprites.Prepared{Port: 41234, SSHAuthSocket: "/tmp/hrd-agent"})
			}
			model := spriteReady(t, ops)
			model.browser.Select(1)
			model, _ = update(t, model, press("enter"))
			if tc.move {
				model, _ = update(t, model, press("down"))
			}
			var native tea.ExecCommand
			var processArgs, processEnv []string
			model.execCommand = func(command tea.ExecCommand, callback tea.ExecCallback) tea.Cmd {
				native = command
				return func() tea.Msg { return callback(nil) }
			}
			model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
				processArgs = append([]string(nil), command.Args...)
				processEnv = append([]string(nil), command.Env...)
				return func() tea.Msg { return callback(nil) }
			}
			model, prepare := update(t, model, press("enter"))
			if model.connection.phase != connectionPreparing || prepare == nil {
				t.Fatalf("prepare phase/cmd = %v/%v", model.active, prepare)
			}
			if tc.transport == catalog.NativePreview {
				view := ansi.Strip(model.View().Content)
				if !strings.Contains(view, "Choose a connection") || strings.Contains(view, "Preparing") || strings.Contains(view, "Waking") || strings.Contains(view, "Herdr has the terminal") {
					t.Fatalf("native preparation changed the chooser: %q", view)
				}
			}
			model, handoff := update(t, model, runFirst(t, prepare))
			if model.connection.phase != connectionAttaching || handoff == nil {
				t.Fatalf("handoff phase/cmd = %v/%v", model.active, handoff)
			}
			if model.home.spriteSnapshot.current || model.sessions.spriteSnapshotDetail.current {
				t.Fatalf("attach retained current observations = endpoint %v sessions %v", model.home.spriteSnapshot.current, model.sessions.spriteSnapshotDetail.current)
			}
			if tc.transport == catalog.NativePreview {
				view := ansi.Strip(model.View().Content)
				if !strings.Contains(view, "Choose a connection") || strings.Contains(view, "Preparing") || strings.Contains(view, "devbox sessions") {
					t.Fatalf("native handoff changed surfaces: %q", view)
				}
				if native != terminal || len(processArgs) != 0 {
					t.Fatalf("native handoff = %T, process args %#v", native, processArgs)
				}
			} else {
				want := []string{"herdr", "--remote", "ssh://sprite@127.0.0.1:41234", "--session", "work"}
				if !reflect.DeepEqual(processArgs, want) {
					t.Fatalf("SSH Herdr args = %#v, want %#v", processArgs, want)
				}
				if !contains(processEnv, "SSH_AUTH_SOCK=/tmp/hrd-agent") || containsPrefix(processEnv, "SPRITE_TOKEN=") {
					t.Fatalf("SSH child environment = %#v", processEnv)
				}
			}
		})
	}
}

func TestBackAfterNativeAttachReconcilesSpriteLifecycle(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.inventoryFn = func(_ context.Context, call int) catalog.Inventory {
		lifecycle := sprites.Cold
		if call > 1 {
			lifecycle = sprites.Running
		}
		return catalog.Inventory{
			Local:   catalog.SourceResult{Rows: localRows()},
			Sprites: catalog.SourceResult{Rows: spriteEndpointRows("devbox", lifecycle)},
		}
	}
	ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return catalog.PreparedNativeAttach{Command: &fakeTerminalCommand{}}, nil
	}
	model := ready(t, ops)
	model.browser.Select(2)
	model, load := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, load))
	model, _ = update(t, model, press("enter"))
	model.execCommand = func(tea.ExecCommand, tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return attachDone{opID: model.operation.id} }
	}
	model, prepare := update(t, model, press("enter"))
	model, attach := update(t, model, runFirst(t, prepare))
	model, sessions := update(t, model, run(t, attach))
	model, _ = update(t, model, runFirst(t, sessions))
	if model.home.spriteSnapshot.current {
		t.Fatal("successful native attach left pre-attach provider lifecycle current")
	}

	model, reconcile := update(t, model, press("esc"))
	if reconcile == nil || !model.home.inventoryRefreshing || model.home.spriteSnapshot.current || !strings.Contains(ansi.Strip(model.View().Content), "stale") {
		t.Fatalf("back did not invalidate and reconcile lifecycle: refreshing %v current %v view %q cmd %v", model.home.inventoryRefreshing, model.home.spriteSnapshot.current, model.View().Content, reconcile)
	}
	model, _ = update(t, model, runFirst(t, reconcile))
	if !model.home.spriteSnapshot.current || !strings.Contains(ansi.Strip(model.View().Content), "running") || strings.Contains(ansi.Strip(model.View().Content), "cold") {
		t.Fatalf("reconciled lifecycle = current %v view %q", model.home.spriteSnapshot.current, model.View().Content)
	}
}

func TestStopConfirmationAndRestartComposeStopThenAttach(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	model := spriteReady(t, ops)
	model, cmd := update(t, model, press("s"))
	if cmd != nil || model.connection.phase != connectionConfirmingStop || model.connection.restart {
		t.Fatalf("stop confirmation = phase %v restart %v cmd %v", model.active, model.connection.restart, cmd)
	}
	if !strings.Contains(model.View().Content, "Pane processes in this session will exit") {
		t.Fatalf("confirmation lacks process warning: %q", model.View().Content)
	}
	model, _ = update(t, model, press("n"))
	if !model.ready() || ops.stopCalls.Load() != 0 {
		t.Fatal("cancel stopped the session")
	}

	model, _ = update(t, model, press("R"))
	model, stop := update(t, model, press("y"))
	if stop != nil || model.connection.phase != connectionChoosingTransport || ops.stopCalls.Load() != 0 {
		t.Fatalf("restart transport choice = calls %d phase %v cmd %v", ops.stopCalls.Load(), model.active, stop)
	}
	model, stop = update(t, model, press("enter"))
	model, follow := update(t, model, runFirst(t, stop))
	if follow == nil || ops.stopCalls.Load() != 1 || model.connection.phase != connectionPreparing || model.sessions.spriteSnapshotDetail.current {
		t.Fatalf("restart composition = calls %d phase %v current %v cmd %v", ops.stopCalls.Load(), model.active, model.sessions.spriteSnapshotDetail.current, follow)
	}
}

func TestRestartTransportCancelDoesNotStopAndPrepareFailureRefreshesWithNotice(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, errors.New("native setup failed")
	}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("R"))
	model, _ = update(t, model, press("y"))
	model, cmd := update(t, model, press("esc"))
	if cmd != nil || !model.ready() || ops.stopCalls.Load() != 0 {
		t.Fatalf("transport cancel = phase %v stops %d cmd %v", model.active, ops.stopCalls.Load(), cmd)
	}

	model, _ = update(t, model, press("R"))
	model, _ = update(t, model, press("y"))
	model, stop := update(t, model, press("enter"))
	model, prepare := update(t, model, runFirst(t, stop))
	if model.sessions.spriteSnapshotDetail.current || model.connection.phase != connectionPreparing || prepare == nil {
		t.Fatalf("post-stop invalidation = current %v phase %v cmd %v", model.sessions.spriteSnapshotDetail.current, model.active, prepare)
	}
	model, refresh := update(t, model, runFirst(t, prepare))
	if model.sessions.phase != sessionsRefreshing || refresh == nil || !strings.Contains(model.notice, "native setup failed") {
		t.Fatalf("prepare failure refresh = phase %v notice %q cmd %v", model.active, model.notice, refresh)
	}
	model, _ = update(t, model, runFirst(t, refresh))
	if !model.ready() || !model.sessions.spriteSnapshotDetail.current || !strings.Contains(model.View().Content, "native setup failed") {
		t.Fatalf("post-failure refresh = phase %v current %v view %q", model.active, model.sessions.spriteSnapshotDetail.current, model.View().Content)
	}
}

func TestSSHRestartKeepsRecoveryAfterStopping(t *testing.T) {
	selection := &sshagent.SelectionRequired{Source: "1Password", Identities: []sshagent.Identity{
		{Fingerprint: "SHA256:work", Comment: "Work", Algorithm: "ssh-ed25519"},
		{Fingerprint: "SHA256:personal", Comment: "Personal", Algorithm: "ssh-ed25519"},
	}}

	for _, tc := range []struct {
		name       string
		prepareErr error
		wantPhase  connectionPhase
	}{
		{name: "key selection", prepareErr: selection, wantPhase: connectionChoosingSSHKey},
		{name: "ssh-add", prepareErr: sshagent.ErrNoKeys, wantPhase: connectionStartingSSHAgent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
				return nil, tc.prepareErr
			}
			model := spriteReady(t, ops)
			model, _ = update(t, model, press("R"))
			model, _ = update(t, model, press("y"))
			model, _ = update(t, model, press("down"))
			model, stop := update(t, model, press("enter"))
			model, prepare := update(t, model, runFirst(t, stop))
			model, recovery := update(t, model, runFirst(t, prepare))
			if model.connection.phase != tc.wantPhase || !model.connection.restartStopped {
				t.Fatalf("post-stop recovery = phase %v stopped %v cmd %v", model.connection.phase, model.connection.restartStopped, recovery)
			}

			if tc.wantPhase == connectionChoosingSSHKey {
				model, recovery = update(t, model, press("esc"))
			} else {
				model, recovery = update(t, model, run(t, recovery))
			}
			if model.sessions.phase != sessionsRefreshing || model.connection.restartStopped || recovery == nil {
				t.Fatalf("recovery exit = phase %v stopped %v cmd %v", model.sessions.phase, model.connection.restartStopped, recovery)
			}
			model, _ = update(t, model, runFirst(t, recovery))
			if !model.ready() || !model.sessions.spriteSnapshotDetail.current {
				t.Fatalf("reconciled recovery exit = active %v sessions phase %v", model.active, model.sessions.phase)
			}
		})
	}
}

func TestAttachFailureNoticeSurvivesAutomaticRefresh(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transport catalog.SpriteTransport
	}{
		{name: "native", transport: catalog.NativePreview},
		{name: "process", transport: catalog.SSHFallback},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("SPRITE_TOKEN", "must-not-reach-child")
			ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
			ops.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
				if tc.transport == catalog.NativePreview {
					return catalog.PreparedNativeAttach{Command: &fakeTerminalCommand{}}, nil
				}
				return catalog.PrepareSpriteSSH("devbox", "default", sprites.Prepared{Port: 22})
			}
			model := spriteReady(t, ops)
			model, _ = update(t, model, press("enter"))
			if tc.transport == catalog.SSHFallback {
				model, _ = update(t, model, press("down"))
			}
			model.execCommand = func(tea.ExecCommand, tea.ExecCallback) tea.Cmd {
				return func() tea.Msg {
					return attachDone{opID: model.operation.id, err: errors.New("native control failure\nunsafe")}
				}
			}
			model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
				if containsPrefix(command.Env, "SPRITE_TOKEN=") {
					t.Fatalf("process environment contains SPRITE_TOKEN: %#v", command.Env)
				}
				return func() tea.Msg { return callback(errors.New("process control failure\nunsafe")) }
			}
			model, prepare := update(t, model, press("enter"))
			model, attach := update(t, model, runFirst(t, prepare))
			model, refresh := update(t, model, run(t, attach))
			if model.sessions.phase != sessionsRefreshing || refresh == nil || !strings.Contains(model.notice, "control failureunsafe") {
				t.Fatalf("attach failure = phase %v notice %q cmd %v", model.active, model.notice, refresh)
			}
			model, _ = update(t, model, runFirst(t, refresh))
			if !strings.Contains(model.View().Content, "control failureunsafe") {
				t.Fatalf("successful refresh cleared attach failure: %q", model.View().Content)
			}
		})
	}
}

func TestHostKeyRequiresExplicitConfirmationAndCancelDropsPrepared(t *testing.T) {
	ops := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	ops.prepareSpriteFn = func(_ context.Context, _ catalog.SpriteSessionSelection, _ catalog.SpriteTransport, _ int) (catalog.PreparedAttach, error) {
		return catalog.PrepareSpriteSSH("devbox", "default", sprites.Prepared{Port: 22, Fingerprint: "SHA256:verified"})
	}
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	execCalls := 0
	model.execProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd { execCalls++; return nil }
	model, prepare := update(t, model, press("enter"))
	model, cmd := update(t, model, runFirst(t, prepare))
	if cmd != nil || model.connection.phase != connectionConfirmingHostKey || execCalls != 0 || !strings.Contains(model.View().Content, "SHA256:verified") {
		t.Fatalf("host confirmation = phase %v calls %d cmd %v view %q", model.active, execCalls, cmd, model.View().Content)
	}
	model, refresh := update(t, model, press("esc"))
	if model.connection.prepared != nil || model.sessions.phase != sessionsRefreshing || refresh == nil {
		t.Fatalf("cancel cleanup = prepared %v phase %v cmd %v", model.connection.prepared, model.active, refresh)
	}
}

func TestSSHKeySelectionUsesExactlyOneKeyAndRetries(t *testing.T) {
	selection := &sshagent.SelectionRequired{Source: "1Password", Identities: []sshagent.Identity{
		{Fingerprint: "SHA256:work", Comment: "Work", Algorithm: "ssh-ed25519"},
		{Fingerprint: "SHA256:personal", Comment: "Personal", Algorithm: "ssh-ed25519"},
	}}
	base := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	base.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, selection
	}
	ops := base
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	model, prepare := update(t, model, press("enter"))
	model, _ = update(t, model, runFirst(t, prepare))
	if model.connection.phase != connectionChoosingSSHKey || !strings.Contains(model.View().Content, "Choose exactly one") {
		t.Fatalf("SSH selection phase/view = %v/%q", model.active, model.View().Content)
	}
	model, _ = update(t, model, press("down"))
	model, retry := update(t, model, press("enter"))
	if ops.selected != "SHA256:personal" || model.connection.phase != connectionPreparing || retry == nil {
		t.Fatalf("key selection/retry = %q/%v/%v", ops.selected, model.active, retry)
	}
}

func TestSSHAddRecoveryRunsOnce(t *testing.T) {
	base := &fakeSpriteOperations{fakeOperations: &fakeOperations{}}
	base.prepareSpriteFn = func(context.Context, catalog.SpriteSessionSelection, catalog.SpriteTransport, int) (catalog.PreparedAttach, error) {
		return nil, sshagent.ErrNoKeys
	}
	base.addCommand = exec.Command("ssh-add")
	ops := base
	model := spriteReady(t, ops)
	model, _ = update(t, model, press("enter"))
	model, _ = update(t, model, press("down"))
	model, prepare := update(t, model, press("enter"))
	model, start := update(t, model, runFirst(t, prepare))
	if model.connection.phase != connectionStartingSSHAgent || start == nil || !model.connection.sshRecoveryTried {
		t.Fatalf("recovery start = phase %v cmd %v tried %v", model.active, start, model.connection.sshRecoveryTried)
	}
	model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		if !reflect.DeepEqual(command.Args, []string{"ssh-add"}) {
			t.Fatalf("ssh-add command args = %#v", command.Args)
		}
		return func() tea.Msg { return callback(nil) }
	}
	model, add := update(t, model, run(t, start))
	model, retry := update(t, model, run(t, add))
	if retry == nil || model.connection.phase != connectionPreparing || ops.addCalls.Load() != 1 || base.prepareSpriteCalls.Load() != 1 {
		t.Fatalf("ssh-add retry = phase %v add %d prepare %d cmd %v", model.active, ops.addCalls.Load(), base.prepareSpriteCalls.Load(), retry)
	}
	model, noSecondRecovery := update(t, model, runFirst(t, retry))
	if noSecondRecovery != nil || !model.ready() || ops.addCalls.Load() != 1 || base.prepareSpriteCalls.Load() != 2 {
		t.Fatalf("second failure = phase %v add %d prepare %d cmd %v", model.active, ops.addCalls.Load(), base.prepareSpriteCalls.Load(), noSecondRecovery)
	}
}

func TestQuitCancelsEffectAndRejectsLatePreparedCompletion(t *testing.T) {
	started := make(chan struct{})
	ops := &fakeOperations{prepareFn: func(ctx context.Context, destination catalog.Destination, _ int) (catalog.PreparedAttach, error) {
		close(started)
		<-ctx.Done()
		return catalog.Prepare(destination)
	}}
	model := ready(t, ops)
	model, prepare := update(t, model, press("enter"))
	done := make(chan tea.Msg, 1)
	go func() { done <- runFirst(t, prepare) }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prepare did not start")
	}
	model, quit := update(t, model, press("q"))
	if !model.quitting || quit == nil {
		t.Fatal("quit did not cancel preparation")
	}
	select {
	case completion := <-done:
		model, late := update(t, model, completion)
		if late != nil || !model.quitting || model.connection.prepared != nil {
			t.Fatal("late prepared completion survived quit cleanup")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled preparation did not return")
	}
}
