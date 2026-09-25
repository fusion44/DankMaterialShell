package notify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	notifyDest      = "org.freedesktop.Notifications"
	notifyPath      = "/org/freedesktop/Notifications"
	notifyInterface = "org.freedesktop.Notifications"

	maxSummaryLen = 29
	maxBodyLen    = 80

	listenerMaxLifetime = time.Hour
	notifyCallTimeout   = 5 * time.Second
)

var defaultActions = newActionWatcher()

type Notification struct {
	AppName      string
	Icon         string
	Summary      string
	Body         string
	FilePath     string
	ActionTarget string
	Timeout      int32
	Persistent   bool
	OpenLabel    string
}

func Send(n Notification) (uint32, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return 0, fmt.Errorf("dbus session failed: %w", err)
	}

	if n.AppName == "" {
		n.AppName = "DMS"
	}
	if n.Timeout == 0 && !n.Persistent {
		n.Timeout = 5000
	}

	n.Summary = truncate(n.Summary, maxSummaryLen)
	n.Body = truncate(n.Body, maxBodyLen)

	actionTarget := n.ActionTarget
	if actionTarget == "" {
		actionTarget = n.FilePath
	}

	var actions []string
	if actionTarget != "" {
		openLabel := n.OpenLabel
		if openLabel == "" {
			openLabel = "Open"
		}
		actions = []string{"open", openLabel}
		if n.OpenLabel == "" && n.ActionTarget == "" {
			actions = append(actions, "folder", "Open Folder")
		}
	}

	hints := map[string]dbus.Variant{}
	if n.FilePath != "" {
		imgPath := n.FilePath
		if !strings.HasPrefix(imgPath, "file://") {
			imgPath = "file://" + imgPath
		}
		hints["image_path"] = dbus.MakeVariant(imgPath)
	}

	obj := conn.Object(notifyDest, notifyPath)
	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	call := obj.CallWithContext(
		ctx,
		notifyInterface+".Notify",
		0,
		n.AppName,
		uint32(0),
		n.Icon,
		n.Summary,
		n.Body,
		actions,
		hints,
		n.Timeout,
	)

	if call.Err != nil {
		return 0, fmt.Errorf("notify call failed: %w", call.Err)
	}

	var notificationID uint32
	if err := call.Store(&notificationID); err != nil {
		return 0, fmt.Errorf("failed to get notification id: %w", err)
	}

	return notificationID, nil
}

func SendActionable(n Notification) (uint32, error) {
	actionTarget := n.ActionTarget
	if actionTarget == "" {
		actionTarget = n.FilePath
	}
	if actionTarget == "" {
		return 0, fmt.Errorf("action target required")
	}

	if err := defaultActions.lockRunning(); err != nil {
		return 0, fmt.Errorf("watch notification actions: %w", err)
	}
	defer defaultActions.mu.Unlock()

	id, err := Send(n)
	if err != nil {
		defaultActions.stopIfIdleLocked()
		return 0, err
	}
	if id == 0 {
		defaultActions.stopIfIdleLocked()
		return 0, fmt.Errorf("notification service returned an invalid ID")
	}

	defaultActions.watched[id] = actionTarget
	return id, nil
}

func Close(notificationID uint32) error {
	conn, err := dbus.SessionBus()
	if err != nil {
		return fmt.Errorf("dbus session failed: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), notifyCallTimeout)
	defer cancel()
	call := conn.Object(notifyDest, notifyPath).CallWithContext(ctx, notifyInterface+".CloseNotification", 0, notificationID)
	if call.Err != nil {
		return fmt.Errorf("close notification failed: %w", call.Err)
	}

	return nil
}

func truncate(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit-3]) + "..."
}

func WatchAction(notificationID uint32, target string) error {
	return defaultActions.Watch(notificationID, target)
}

func UnwatchAction(notificationID uint32) {
	defaultActions.take(notificationID)
}

func CloseActionWatcher() {
	defaultActions.Close()
}

func SpawnActionListener(notificationID uint32, filePath string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}

	cmd := exec.Command(exe, "notify-action-generic", fmt.Sprintf("%d", notificationID), filePath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}

func RunActionListener(args []string) {
	if len(args) < 2 {
		return
	}

	notificationID, err := strconv.ParseUint(args[0], 10, 32)
	if err != nil {
		return
	}

	filePath := args[1]

	conn, err := dbus.SessionBus()
	if err != nil {
		return
	}

	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(notifyPath),
		dbus.WithMatchInterface(notifyInterface),
	); err != nil {
		return
	}

	signals := make(chan *dbus.Signal, 10)
	conn.Signal(signals)
	deadline := time.After(listenerMaxLifetime)

	for {
		select {
		case <-deadline:
			return
		case sig := <-signals:
			if sig == nil || handleSignal(sig, uint32(notificationID), filePath) {
				return
			}
		}
	}
}

func handleSignal(sig *dbus.Signal, notificationID uint32, filePath string) bool {
	if len(sig.Body) < 1 {
		return false
	}
	id, ok := sig.Body[0].(uint32)
	if !ok || id != notificationID {
		return false
	}
	switch sig.Name {
	case notifyInterface + ".NotificationClosed":
		return true
	case notifyInterface + ".ActionInvoked":
		if len(sig.Body) < 2 {
			return false
		}
		action, ok := sig.Body[1].(string)
		if !ok {
			return false
		}
		handleAction(action, filePath)
		return true
	}
	return false
}

func handleAction(action, filePath string) {
	switch action {
	case "open", "default":
		openPathFunc(filePath)
	case "folder":
		openPathFunc(filepath.Dir(filePath))
	}
}

type actionWatcher struct {
	mu       sync.Mutex
	watched  map[uint32]string
	started  bool
	stopping bool
	stop     chan struct{}
	done     chan struct{}
}

func newActionWatcher() *actionWatcher {
	return &actionWatcher{watched: make(map[uint32]string)}
}

func (w *actionWatcher) Watch(notificationID uint32, target string) error {
	if notificationID == 0 || target == "" {
		return fmt.Errorf("notification ID and action target are required")
	}
	if err := w.lockRunning(); err != nil {
		return err
	}
	defer w.mu.Unlock()
	w.watched[notificationID] = target
	return nil
}

func (w *actionWatcher) Close() {
	w.mu.Lock()
	w.watched = make(map[uint32]string)
	if w.started {
		w.stopIfIdleLocked()
	}
	done := w.done
	w.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (w *actionWatcher) lockRunning() error {
	for {
		w.mu.Lock()
		if w.started {
			return nil
		}
		if w.stopping {
			done := w.done
			w.mu.Unlock()
			<-done
			continue
		}

		conn, err := dbus.SessionBus()
		if err != nil {
			w.mu.Unlock()
			return err
		}
		matchOptions := []dbus.MatchOption{
			dbus.WithMatchObjectPath(notifyPath),
			dbus.WithMatchInterface(notifyInterface),
		}
		if err := conn.AddMatchSignal(matchOptions...); err != nil {
			w.mu.Unlock()
			return err
		}

		signals := make(chan *dbus.Signal, 32)
		conn.Signal(signals)
		stop := make(chan struct{})
		w.started = true
		w.stop = stop
		go w.run(stop, conn, signals, matchOptions)
		return nil
	}
}

func (w *actionWatcher) run(stop chan struct{}, conn *dbus.Conn, signals chan *dbus.Signal, matchOptions []dbus.MatchOption) {
	defer w.finish(stop)
	defer conn.RemoveMatchSignal(matchOptions...)
	defer conn.RemoveSignal(signals)

	for {
		select {
		case <-stop:
			return
		case sig, ok := <-signals:
			if !ok {
				return
			}
			if sig != nil {
				w.handle(sig)
			}
		}
	}
}

func (w *actionWatcher) finish(stop chan struct{}) {
	w.mu.Lock()
	if w.stop != stop {
		w.mu.Unlock()
		return
	}
	done := w.done
	w.started = false
	w.stopping = false
	w.stop = nil
	w.done = nil
	restart := len(w.watched) > 0
	w.mu.Unlock()
	if done != nil {
		close(done)
	}
	if !restart {
		return
	}
	if err := w.lockRunning(); err != nil {
		w.mu.Lock()
		w.watched = make(map[uint32]string)
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
}

func (w *actionWatcher) stopIfIdleLocked() {
	if len(w.watched) != 0 || !w.started {
		return
	}
	close(w.stop)
	w.started = false
	w.stopping = true
	w.done = make(chan struct{})
}

func (w *actionWatcher) handle(sig *dbus.Signal) {
	if len(sig.Body) < 1 {
		return
	}
	id, ok := sig.Body[0].(uint32)
	if !ok {
		return
	}
	switch sig.Name {
	case notifyInterface + ".NotificationClosed":
		w.take(id)
	case notifyInterface + ".ActionInvoked":
		if len(sig.Body) < 2 {
			return
		}
		action, ok := sig.Body[1].(string)
		if !ok {
			return
		}
		target, ok := w.take(id)
		if ok {
			handleAction(action, target)
		}
	}
}

func (w *actionWatcher) take(id uint32) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	target, ok := w.watched[id]
	if ok {
		delete(w.watched, id)
		w.stopIfIdleLocked()
	}
	return target, ok
}

var openPathFunc = openPath

func openPath(path string) {
	cmd := exec.Command("xdg-open", path)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		return
	}
	go func() { _ = cmd.Wait() }()
}
