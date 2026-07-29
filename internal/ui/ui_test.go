package ui

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/joshuadavidthomas/hrd/internal/catalog"
	"github.com/joshuadavidthomas/hrd/internal/herdr"
	"github.com/joshuadavidthomas/hrd/internal/sprites"
	"github.com/joshuadavidthomas/hrd/internal/sshagent"
)

type fakeOperations struct {
	listCalls    atomic.Int32
	prepareCalls atomic.Int32
	listFn       func(context.Context, int) ([]catalog.Destination, error)
	prepareFn    func(context.Context, catalog.Destination, int) (catalog.PreparedAttach, error)
}

func (f *fakeOperations) List(ctx context.Context) ([]catalog.Destination, error) {
	call := int(f.listCalls.Add(1))
	if f.listFn == nil {
		return testDestinations(), nil
	}
	return f.listFn(ctx, call)
}

func (f *fakeOperations) Prepare(ctx context.Context, destination catalog.Destination) (catalog.PreparedAttach, error) {
	call := int(f.prepareCalls.Add(1))
	if f.prepareFn == nil {
		return catalog.Prepare(destination)
	}
	return f.prepareFn(ctx, destination, call)
}

type fakeSSHKeyOperations struct {
	*fakeOperations
	addCalls atomic.Int32
	command  *exec.Cmd
	err      error
}

func (f *fakeSSHKeyOperations) SSHAddCommand(context.Context) (*exec.Cmd, error) {
	f.addCalls.Add(1)
	return f.command, f.err
}

func TestInitialLoadProducesReadySelectableRows(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{}
	model := New(context.Background(), operations)
	if model.phase != phaseLoading || model.operationID != 1 {
		t.Fatalf("new phase/id = (%v, %d), want (loading, 1)", model.phase, model.operationID)
	}
	if content := model.View().Content; !strings.Contains(content, "Loading destinations") {
		t.Fatalf("loading view = %q", content)
	}

	model, cmd := runUpdate(t, model, runCmd(t, model.Init()))
	if cmd != nil {
		t.Fatal("initial completion returned an unexpected command")
	}
	if model.phase != phaseReady || !model.attachEnabled {
		t.Fatalf("loaded phase/enabled = (%v, %v), want ready/true", model.phase, model.attachEnabled)
	}
	if operations.listCalls.Load() != 1 {
		t.Fatalf("list calls = %d, want 1", operations.listCalls.Load())
	}
	content := model.View().Content
	for _, want := range []string{"Local", "default", "running", "alpha", "stopped", "attach", "refresh", "search", "help"} {
		if !strings.Contains(content, want) {
			t.Errorf("ready view %q does not contain %q", content, want)
		}
	}
}

func TestReadyViewGroupsLocalAndSpriteDestinations(t *testing.T) {
	t.Parallel()
	model := readyModel(t, &fakeOperations{})
	spriteRows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "devbox", State: sprites.Cold}})
	if err != nil {
		t.Fatal(err)
	}
	model.destinations = append(model.destinations, spriteRows...)
	content := model.View().Content
	for _, want := range []string{"2 local", "1 Sprite", "Local", "Sprites", "devbox", "cold"} {
		if !strings.Contains(content, want) {
			t.Errorf("view %q does not contain %q", content, want)
		}
	}
}

func TestMissingAgentNoticeGivesNextAction(t *testing.T) {
	t.Parallel()
	notice := prepareFailureNotice(errors.New("ssh-agent is not available: SSH_AUTH_SOCK is unset"))
	for _, want := range []string{"SSH key required", "ssh-agent", "ssh-add", "try again"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice %q does not contain %q", notice, want)
		}
	}
}

func TestLoadingIgnoresActionKeysWithoutStartingAnotherOperation(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{}
	model := New(context.Background(), operations)
	initialID := model.operationID
	for _, action := range []tea.Msg{press("r"), press("enter"), press("?"), press("/")} {
		var cmd tea.Cmd
		model, cmd = runUpdate(t, model, action)
		if cmd != nil {
			t.Fatalf("loading action %v returned a command", action)
		}
		if model.operationID != initialID {
			t.Fatalf("loading action changed ID to %d, want %d", model.operationID, initialID)
		}
	}
	if operations.listCalls.Load() != 0 {
		t.Fatalf("loading actions ran list: calls = %d", operations.listCalls.Load())
	}
}

func TestRefreshUsesIncreasingIDAndIgnoresDuplicateActionKeys(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{}
	model := readyModel(t, operations)
	initialID := model.operationID

	model, refreshCmd := runUpdate(t, model, press("r"))
	if refreshCmd == nil || model.phase != phaseRefreshing || model.operationID <= initialID {
		t.Fatalf("refresh phase/id/cmd = (%v, %d, %v), want refreshing, increasing, non-nil", model.phase, model.operationID, refreshCmd)
	}
	refreshID := model.operationID
	for _, action := range []tea.Msg{press("r"), press("enter"), press("?"), press("/")} {
		var duplicate tea.Cmd
		model, duplicate = runUpdate(t, model, action)
		if duplicate != nil {
			t.Fatalf("duplicate action %v returned a command", action)
		}
		if model.operationID != refreshID {
			t.Fatalf("duplicate action changed ID to %d, want %d", model.operationID, refreshID)
		}
	}
	if operations.listCalls.Load() != 1 {
		t.Fatalf("duplicate keys ran list before refresh command: calls = %d", operations.listCalls.Load())
	}

	model, _ = runUpdate(t, model, runCmd(t, refreshCmd))
	if model.phase != phaseReady || operations.listCalls.Load() != 2 {
		t.Fatalf("refresh completion phase/calls = (%v, %d), want ready/2", model.phase, operations.listCalls.Load())
	}
}

func TestPrepareExecutesExactHerdrArgsThenRefreshes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		move      tea.Msg
		wantArgs  []string
		wantLabel string
	}{
		{name: "default", wantArgs: []string{"herdr"}, wantLabel: "default"},
		{name: "named", move: press("down"), wantArgs: []string{"herdr", "--session", "alpha"}, wantLabel: "alpha"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operations := &fakeOperations{}
			model := readyModel(t, operations)
			if test.move != nil {
				model, _ = runUpdate(t, model, test.move)
			}

			var gotArgs []string
			var callback tea.ExecCallback
			var execCalls int
			model.execProcess = func(command *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
				execCalls++
				gotArgs = append([]string(nil), command.Args...)
				callback = cb
				return func() tea.Msg { return cb(nil) }
			}

			readyID := model.operationID
			model, prepareCmd := runUpdate(t, model, press("enter"))
			if model.phase != phasePreparing || prepareCmd == nil || model.operationID <= readyID {
				t.Fatalf("enter phase/id/cmd = (%v, %d, %v), want preparing/increasing/non-nil", model.phase, model.operationID, prepareCmd)
			}
			prepareID := model.operationID
			if content := model.View().Content; !strings.Contains(content, "Preparing "+test.wantLabel) {
				t.Fatalf("preparing view = %q", content)
			}

			for _, duplicateKey := range []string{"enter", "r"} {
				var duplicate tea.Cmd
				model, duplicate = runUpdate(t, model, press(duplicateKey))
				if duplicate != nil || model.operationID != prepareID {
					t.Fatalf("duplicate %q returned command or changed ID", duplicateKey)
				}
			}

			model, execCmd := runUpdate(t, model, runCmd(t, prepareCmd))
			if model.phase != phaseAttaching || execCmd == nil {
				t.Fatalf("prepare completion phase/cmd = (%v, %v), want attaching/non-nil", model.phase, execCmd)
			}
			if execCalls != 1 || callback == nil {
				t.Fatalf("exec adapter calls/callback = (%d, %v), want 1/non-nil", execCalls, callback)
			}
			if !reflect.DeepEqual(gotArgs, test.wantArgs) {
				t.Fatalf("exec Cmd.Args = %#v, want %#v", gotArgs, test.wantArgs)
			}

			model, refreshCmd := runUpdate(t, model, runCmd(t, execCmd))
			if model.phase != phaseRefreshing || refreshCmd == nil {
				t.Fatalf("attach completion phase/cmd = (%v, %v), want refreshing/non-nil", model.phase, refreshCmd)
			}
			if model.operationID <= prepareID {
				t.Fatalf("post-attach refresh ID = %d, want > %d", model.operationID, prepareID)
			}
			model, _ = runUpdate(t, model, runCmd(t, refreshCmd))
			if model.phase != phaseReady || operations.listCalls.Load() != 2 {
				t.Fatalf("post-attach phase/list calls = (%v, %d), want ready/2", model.phase, operations.listCalls.Load())
			}
		})
	}
}

func TestSpriteHerdrChildReceivesManagedAgentSocket(t *testing.T) {
	t.Setenv("SPRITE_TOKEN", "must-not-reach-herdr")
	spriteRows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "devbox", State: sprites.Cold}})
	if err != nil {
		t.Fatal(err)
	}
	operations := &fakeOperations{
		listFn: func(context.Context, int) ([]catalog.Destination, error) { return spriteRows, nil },
		prepareFn: func(context.Context, catalog.Destination, int) (catalog.PreparedAttach, error) {
			return catalog.PrepareSprite("devbox", sprites.Prepared{Port: 41234, SSHAuthSocket: "/tmp/hrd-agent.sock"})
		},
	}
	model := readyModel(t, operations)
	var childEnvironment []string
	model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		childEnvironment = append([]string(nil), command.Env...)
		return func() tea.Msg { return callback(nil) }
	}
	model, prepareCmd := runUpdate(t, model, press("enter"))
	_, execCmd := runUpdate(t, model, runCmd(t, prepareCmd))
	if execCmd == nil {
		t.Fatal("Sprite preparation did not hand off to Herdr")
	}
	found := false
	for _, entry := range childEnvironment {
		if entry == "SSH_AUTH_SOCK=/tmp/hrd-agent.sock" {
			found = true
		}
		if strings.HasPrefix(entry, "SPRITE_TOKEN=") {
			t.Fatalf("Herdr environment contains SPRITE_TOKEN: %#v", childEnvironment)
		}
	}
	if !found {
		t.Fatalf("Herdr environment lacks managed socket: %#v", childEnvironment)
	}
}

func TestAttachSuccessAndFailureEachStartExactlyOneRefresh(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		attachErr error
	}{
		{name: "success"},
		{name: "failure", attachErr: errors.New("child exit status 9")},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operations := &fakeOperations{}
			model := readyModel(t, operations)
			model, prepareCmd := runUpdate(t, model, press("enter"))
			model.execProcess = func(_ *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
				return func() tea.Msg { return callback(test.attachErr) }
			}
			model, execCmd := runUpdate(t, model, runCmd(t, prepareCmd))
			model, refreshCmd := runUpdate(t, model, runCmd(t, execCmd))
			if refreshCmd == nil || model.phase != phaseRefreshing {
				t.Fatalf("matching attach completion did not start refresh")
			}
			if operations.listCalls.Load() != 1 {
				t.Fatalf("list ran before refresh command: calls = %d", operations.listCalls.Load())
			}

			attachID := model.operationID - 1
			var duplicate tea.Cmd
			model, duplicate = runUpdate(t, model, attachDone{opID: attachID, err: test.attachErr})
			if duplicate != nil {
				t.Fatal("duplicate attach completion started another refresh")
			}
			_ = runCmd(t, refreshCmd)
			if operations.listCalls.Load() != 2 {
				t.Fatalf("list calls after one refresh command = %d, want 2", operations.listCalls.Load())
			}
			if test.attachErr != nil && !strings.Contains(model.View().Content, "Attach failed: child exit status 9") {
				t.Fatalf("refreshing view lost attach failure: %q", model.View().Content)
			}
		})
	}
}

func TestFailedRefreshAfterAttachKeepsRowsNoticeAndDisablesAttachUntilRetry(t *testing.T) {
	t.Parallel()

	refreshFailure := errors.New("local inventory unavailable")
	operations := &fakeOperations{listFn: func(_ context.Context, call int) ([]catalog.Destination, error) {
		if call == 2 {
			return nil, refreshFailure
		}
		return testDestinations(), nil
	}}
	model := readyModel(t, operations)
	model, prepareCmd := runUpdate(t, model, press("enter"))
	model.execProcess = func(_ *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return callback(errors.New("attach broke")) }
	}
	model, execCmd := runUpdate(t, model, runCmd(t, prepareCmd))
	model, refreshCmd := runUpdate(t, model, runCmd(t, execCmd))
	if content := model.View().Content; !strings.Contains(content, "Attach failed: attach broke") {
		t.Fatalf("refresh view = %q, want attach failure", content)
	}
	model, _ = runUpdate(t, model, runCmd(t, refreshCmd))
	if model.phase != phaseReady || model.attachEnabled || len(model.destinations) != 2 {
		t.Fatalf("failed refresh phase/enabled/rows = (%v, %v, %d), want ready/false/2", model.phase, model.attachEnabled, len(model.destinations))
	}
	content := model.View().Content
	for _, want := range []string{"default", "alpha", "Attach failed: attach broke", "Refresh failed: local inventory unavailable", "retry"} {
		if !strings.Contains(content, want) {
			t.Errorf("failed refresh view %q does not contain %q", content, want)
		}
	}
	if _, attachCmd := runUpdate(t, model, press("enter")); attachCmd != nil {
		t.Fatal("enter attached while refresh failure disabled attach")
	}

	model, retryCmd := runUpdate(t, model, press("r"))
	model, _ = runUpdate(t, model, runCmd(t, retryCmd))
	if !model.attachEnabled || model.phase != phaseReady {
		t.Fatalf("successful retry phase/enabled = (%v, %v), want ready/true", model.phase, model.attachEnabled)
	}
}

func TestInitialFailureRetriesAndStaleCompletionCannotReplaceRetry(t *testing.T) {
	t.Parallel()

	failure := errors.New("herdr missing")
	operations := &fakeOperations{listFn: func(_ context.Context, call int) ([]catalog.Destination, error) {
		if call == 1 {
			return nil, failure
		}
		return testDestinations(), nil
	}}
	model := New(context.Background(), operations)
	firstID := model.operationID
	model, _ = runUpdate(t, model, runCmd(t, model.Init()))
	if model.phase != phaseInitialLoadFailed || model.attachEnabled {
		t.Fatalf("initial failure phase/enabled = (%v, %v)", model.phase, model.attachEnabled)
	}
	for _, want := range []string{"Couldn’t load destinations", "herdr missing", "retry"} {
		if !strings.Contains(model.View().Content, want) {
			t.Errorf("failure view %q does not contain %q", model.View().Content, want)
		}
	}

	model, retryCmd := runUpdate(t, model, press("r"))
	if model.phase != phaseLoading || model.operationID <= firstID || retryCmd == nil {
		t.Fatalf("retry phase/id/cmd = (%v, %d, %v)", model.phase, model.operationID, retryCmd)
	}
	retryID := model.operationID
	model, staleCmd := runUpdate(t, model, listDone{opID: firstID, destinations: nil})
	if staleCmd != nil || model.phase != phaseLoading || model.operationID != retryID {
		t.Fatal("stale initial completion changed retry load")
	}
	model, _ = runUpdate(t, model, runCmd(t, retryCmd))
	if model.phase != phaseReady || !model.attachEnabled {
		t.Fatalf("retry completion phase/enabled = (%v, %v), want ready/true", model.phase, model.attachEnabled)
	}
}

func TestStalePreparationAndAttachmentCompletionsDoNothing(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{}
	model := readyModel(t, operations)
	model, prepareCmd := runUpdate(t, model, press("enter"))
	preparingID := model.operationID
	model, cmd := runUpdate(t, model, prepareDone{opID: preparingID - 1, destination: model.destinations[0]})
	if cmd != nil || model.phase != phasePreparing {
		t.Fatal("stale prepare completion changed phase")
	}

	model.execProcess = func(_ *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		return func() tea.Msg { return callback(nil) }
	}
	model, _ = runUpdate(t, model, runCmd(t, prepareCmd))
	if model.phase != phaseAttaching {
		t.Fatalf("phase = %v, want attaching", model.phase)
	}
	model, cmd = runUpdate(t, model, attachDone{opID: preparingID - 1})
	if cmd != nil || model.phase != phaseAttaching {
		t.Fatal("stale attach completion changed phase")
	}
	for _, action := range []string{"q", "ctrl+c", "r", "enter"} {
		var ignored tea.Cmd
		model, ignored = runUpdate(t, model, press(action))
		if ignored != nil || model.phase != phaseAttaching {
			t.Fatalf("attaching handled launcher key %q", action)
		}
	}
}

func TestQuitCancelsBlockedListAndPreparation(t *testing.T) {
	t.Parallel()

	t.Run("initial list with q", func(t *testing.T) {
		t.Parallel()
		assertBlockedListCanceled(t, "q")
	})
	t.Run("initial list with ctrl+c", func(t *testing.T) {
		t.Parallel()
		assertBlockedListCanceled(t, "ctrl+c")
	})
	t.Run("preparation", func(t *testing.T) {
		t.Parallel()

		started := make(chan struct{})
		operations := &fakeOperations{prepareFn: func(ctx context.Context, _ catalog.Destination, _ int) (catalog.PreparedAttach, error) {
			close(started)
			<-ctx.Done()
			return catalog.PreparedAttach{}, ctx.Err()
		}}
		model := readyModel(t, operations)
		model, prepareCmd := runUpdate(t, model, press("enter"))
		done := runCmdAsync(prepareCmd)
		awaitSignal(t, started, "preparation start")

		quitting, quitCmd := runUpdate(t, model, press("q"))
		assertQuitCmd(t, quitCmd)
		message := awaitMessage(t, done, "canceled preparation")
		completion, ok := message.(prepareDone)
		if !ok || !errors.Is(completion.err, context.Canceled) {
			t.Fatalf("preparation completion = %#v, want context cancellation", message)
		}
		quitting, staleCmd := runUpdate(t, quitting, completion)
		if staleCmd != nil || !quitting.quitting || quitting.phase != phasePreparing {
			t.Fatal("completion arriving after preparation cancellation changed the model")
		}
	})
}

func TestRefreshQuitCancelsBlockedListing(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	operations := &fakeOperations{listFn: func(ctx context.Context, call int) ([]catalog.Destination, error) {
		if call == 1 {
			return testDestinations(), nil
		}
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	model := readyModel(t, operations)
	model, refreshCmd := runUpdate(t, model, press("r"))
	done := runCmdAsync(refreshCmd)
	awaitSignal(t, started, "refresh start")
	quitting, quitCmd := runUpdate(t, model, press("ctrl+c"))
	assertQuitCmd(t, quitCmd)
	completion := awaitMessage(t, done, "canceled refresh").(listDone)
	if !errors.Is(completion.err, context.Canceled) {
		t.Fatalf("refresh error = %v, want context.Canceled", completion.err)
	}
	quitting, staleCmd := runUpdate(t, quitting, completion)
	if staleCmd != nil || !quitting.quitting || quitting.phase != phaseRefreshing {
		t.Fatal("completion arriving after refresh cancellation changed the model")
	}
}

func TestSpriteNameAllowsQAndOnlyControlCQuits(t *testing.T) {
	t.Parallel()
	model := readyModel(t, &fakeOperations{})
	model.phase = phaseNamingSprite
	model, cmd := runUpdate(t, model, press("q"))
	if cmd != nil || model.quitting || model.spriteName != "q" {
		t.Fatalf("name entry after q = (cmd %v, quitting %v, name %q)", cmd, model.quitting, model.spriteName)
	}
	_, quitCmd := runUpdate(t, model, press("ctrl+c"))
	assertQuitCmd(t, quitCmd)
}

func TestFilteringMakesQEditableAndOnlyControlCQuits(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{}
	model := readyModel(t, operations)
	model, cmd := runUpdate(t, model, press("/"))
	if cmd != nil || !model.editingFilter {
		t.Fatal("/ did not enter filter editing")
	}
	for _, input := range []string{"q", "r", "/", "?"} {
		model, cmd = runUpdate(t, model, press(input))
		if cmd != nil || !model.editingFilter {
			t.Fatalf("filter input %q quit or stopped editing", input)
		}
	}
	if model.filter != "qr/?" {
		t.Fatalf("filter = %q, want %q", model.filter, "qr/?")
	}
	if !strings.Contains(model.View().Content, "ctrl+c") {
		t.Fatalf("filter view = %q", model.View().Content)
	}
	model, _ = runUpdate(t, model, press("enter"))
	if model.editingFilter || model.phase != phaseReady {
		t.Fatal("filter enter attached instead of applying filter")
	}

	model, _ = runUpdate(t, model, press("/"))
	_, quitCmd := runUpdate(t, model, press("ctrl+c"))
	assertQuitCmd(t, quitCmd)

	plain := readyModel(t, &fakeOperations{})
	_, quitCmd = runUpdate(t, plain, press("q"))
	assertQuitCmd(t, quitCmd)
}

func TestHelpAndFilterKeysHaveTheirDocumentedActions(t *testing.T) {
	t.Parallel()

	model := readyModel(t, &fakeOperations{})
	model, _ = runUpdate(t, model, press("?"))
	if !model.showHelp || !strings.Contains(model.View().Content, "close help") {
		t.Fatalf("? did not open help: %q", model.View().Content)
	}
	model, _ = runUpdate(t, model, press("/"))
	for _, letter := range []string{"a", "l", "p", "h", "a"} {
		model, _ = runUpdate(t, model, press(letter))
	}
	model, _ = runUpdate(t, model, press("enter"))
	visible := model.visibleDestinations()
	if len(visible) != 1 || visible[0].Label != "alpha" {
		t.Fatalf("filtered destinations = %#v, want alpha", visible)
	}
}

func TestMissingAgentRunsSSHAddThenRetriesSpritePreparationOnce(t *testing.T) {
	t.Parallel()
	spriteRows, err := catalog.SpriteDestinations([]sprites.Sprite{{Name: "devbox", State: sprites.Cold}})
	if err != nil {
		t.Fatal(err)
	}
	base := &fakeOperations{
		listFn: func(context.Context, int) ([]catalog.Destination, error) { return spriteRows, nil },
		prepareFn: func(context.Context, catalog.Destination, int) (catalog.PreparedAttach, error) {
			return catalog.PreparedAttach{}, sshagent.ErrNoKeys
		},
	}
	operations := &fakeSSHKeyOperations{fakeOperations: base, command: exec.Command("ssh-add")}
	model := New(context.Background(), operations)
	model, _ = runUpdate(t, model, runCmd(t, model.Init()))

	model, prepareCmd := runUpdate(t, model, press("enter"))
	model, startAgentCmd := runUpdate(t, model, runCmd(t, prepareCmd))
	if model.phase != phaseStartingSSHAgent || startAgentCmd == nil || !model.sshRecoveryAttempted {
		t.Fatalf("agent recovery phase/cmd/attempted = (%v, %v, %v)", model.phase, startAgentCmd, model.sshRecoveryAttempted)
	}

	var sshAddArgs []string
	model.execProcess = func(command *exec.Cmd, callback tea.ExecCallback) tea.Cmd {
		sshAddArgs = append([]string(nil), command.Args...)
		return func() tea.Msg { return callback(nil) }
	}
	model, sshAddCmd := runUpdate(t, model, runCmd(t, startAgentCmd))
	if model.phase != phaseAddingSSHKey || sshAddCmd == nil {
		t.Fatalf("ssh-add handoff phase/cmd = (%v, %v)", model.phase, sshAddCmd)
	}
	if want := []string{"ssh-add"}; !reflect.DeepEqual(sshAddArgs, want) {
		t.Fatalf("ssh-add args = %#v, want %#v", sshAddArgs, want)
	}

	model, retryPrepareCmd := runUpdate(t, model, runCmd(t, sshAddCmd))
	if model.phase != phasePreparing || retryPrepareCmd == nil {
		t.Fatalf("retry phase/cmd = (%v, %v)", model.phase, retryPrepareCmd)
	}
	model, refreshCmd := runUpdate(t, model, runCmd(t, retryPrepareCmd))
	if model.phase != phaseRefreshing || refreshCmd == nil {
		t.Fatalf("second key failure phase/cmd = (%v, %v), want one refresh", model.phase, refreshCmd)
	}
	if operations.addCalls.Load() != 1 || base.prepareCalls.Load() != 2 {
		t.Fatalf("ssh-add/prepare calls = (%d, %d), want (1, 2)", operations.addCalls.Load(), base.prepareCalls.Load())
	}
}

func TestPrepareFailureReturnsToReadyWithoutExec(t *testing.T) {
	t.Parallel()

	operations := &fakeOperations{prepareFn: func(context.Context, catalog.Destination, int) (catalog.PreparedAttach, error) {
		return catalog.PreparedAttach{}, errors.New("cannot prepare")
	}}
	model := readyModel(t, operations)
	execCalls := 0
	model.execProcess = func(*exec.Cmd, tea.ExecCallback) tea.Cmd {
		execCalls++
		return nil
	}
	model, prepareCmd := runUpdate(t, model, press("enter"))
	model, cmd := runUpdate(t, model, runCmd(t, prepareCmd))
	if cmd != nil || execCalls != 0 || model.phase != phaseReady || !model.attachEnabled {
		t.Fatalf("prepare failure cmd/exec/phase/enabled = (%v, %d, %v, %v)", cmd, execCalls, model.phase, model.attachEnabled)
	}
	if !strings.Contains(model.View().Content, "Couldn’t prepare destination: cannot prepare") {
		t.Fatalf("prepare failure view = %q", model.View().Content)
	}
}

func TestRenderedSourceTextCannotReorderTerminalContent(t *testing.T) {
	t.Parallel()

	destinations := testDestinations()
	destinations[0].Label = "safe\u202eevil\u2066"
	operations := &fakeOperations{listFn: func(context.Context, int) ([]catalog.Destination, error) {
		return destinations, nil
	}}
	model := readyModel(t, operations)
	content := model.View().Content
	if strings.ContainsAny(content, "\u202e\u2066") {
		t.Fatalf("view retained bidi formatting: %q", content)
	}
	if !strings.Contains(content, "safeevil") {
		t.Fatalf("view lost printable label text: %q", content)
	}
}

func TestLoadingEmptyFailureAndNarrowViewsStayReadable(t *testing.T) {
	t.Parallel()

	loading := New(context.Background(), &fakeOperations{})
	loading, _ = runUpdate(t, loading, tea.WindowSizeMsg{Width: 8, Height: 3})
	assertReadableView(t, loading, "Loadin")
	assertViewFits(t, loading.View(), 8, 3)

	emptyOps := &fakeOperations{listFn: func(context.Context, int) ([]catalog.Destination, error) {
		return []catalog.Destination{}, nil
	}}
	empty := readyModel(t, emptyOps)
	empty, _ = runUpdate(t, empty, tea.WindowSizeMsg{Width: 8, Height: 3})
	assertReadableView(t, empty, "No des")
	assertViewFits(t, empty.View(), 8, 3)
	if _, cmd := runUpdate(t, empty, press("enter")); cmd != nil {
		t.Fatal("empty ready view allowed attach")
	}

	failure := New(context.Background(), &fakeOperations{listFn: func(context.Context, int) ([]catalog.Destination, error) {
		return nil, errors.New("failed")
	}})
	failure, _ = runUpdate(t, failure, runCmd(t, failure.Init()))
	failure, _ = runUpdate(t, failure, tea.WindowSizeMsg{Width: 8, Height: 3})
	assertReadableView(t, failure, "Couldn")
	assertViewFits(t, failure.View(), 8, 3)
}

func assertBlockedListCanceled(t *testing.T, quitKey string) {
	t.Helper()
	started := make(chan struct{})
	operations := &fakeOperations{listFn: func(ctx context.Context, _ int) ([]catalog.Destination, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	model := New(context.Background(), operations)
	done := runCmdAsync(model.Init())
	awaitSignal(t, started, "list start")
	quitting, quitCmd := runUpdate(t, model, press(quitKey))
	assertQuitCmd(t, quitCmd)
	completion := awaitMessage(t, done, "canceled list").(listDone)
	if !errors.Is(completion.err, context.Canceled) {
		t.Fatalf("list error = %v, want context.Canceled", completion.err)
	}
	quitting, staleCmd := runUpdate(t, quitting, completion)
	if staleCmd != nil || !quitting.quitting || quitting.phase != phaseLoading {
		t.Fatal("completion arriving after list cancellation changed the model")
	}
}

func testDestinations() []catalog.Destination {
	destinations, err := catalog.LocalDestinations([]herdr.Session{
		{Name: "default", Default: true, Running: true},
		{Name: "alpha", Running: false},
	})
	if err != nil {
		panic(err)
	}
	return destinations
}

func readyModel(t *testing.T, operations *fakeOperations) Model {
	t.Helper()
	model := New(context.Background(), operations)
	model, _ = runUpdate(t, model, runCmd(t, model.Init()))
	if model.phase != phaseReady {
		t.Fatalf("initial phase = %v, want ready", model.phase)
	}
	return model
}

func press(name string) tea.KeyPressMsg {
	key := tea.Key{Text: name}
	switch name {
	case "enter":
		key = tea.Key{Code: tea.KeyEnter}
	case "up":
		key = tea.Key{Code: tea.KeyUp}
	case "down":
		key = tea.Key{Code: tea.KeyDown}
	case "ctrl+c":
		key = tea.Key{Code: 'c', Mod: tea.ModCtrl}
	case "esc":
		key = tea.Key{Code: tea.KeyEscape}
	case "backspace":
		key = tea.Key{Code: tea.KeyBackspace}
	default:
		runes := []rune(name)
		if len(runes) == 1 {
			key.Code = runes[0]
		}
	}
	return tea.KeyPressMsg(key)
}

func runUpdate(t *testing.T, model Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := model.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("Update model = %T, want ui.Model", updated)
	}
	return result, cmd
}

func runCmd(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("cannot run nil command")
	}
	return cmd()
}

func runCmdAsync(cmd tea.Cmd) <-chan tea.Msg {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	return done
}

func awaitSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitMessage(t *testing.T, messages <-chan tea.Msg, name string) tea.Msg {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", name)
		return nil
	}
}

func assertQuitCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("quit returned nil command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("quit command message = %T, want tea.QuitMsg", cmd())
	}
}

func assertReadableView(t *testing.T, model Model, want string) {
	t.Helper()
	view := model.View()
	if view.Content == "" || !strings.Contains(view.Content, "hrd") || !strings.Contains(view.Content, want) {
		t.Fatalf("view content = %q, want hrd and %q", view.Content, want)
	}
}

func assertViewFits(t *testing.T, view tea.View, width, height int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(view.Content, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines, want at most %d: %q", len(lines), height, view.Content)
	}
	for index, line := range lines {
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("view line %d has width %d, want at most %d: %q", index, got, width, line)
		}
	}
}
