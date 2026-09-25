package notify

import (
	"path/filepath"
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/stretchr/testify/assert"
)

func TestActionWatcherStopsWhenLastNotificationCloses(t *testing.T) {
	stop := make(chan struct{})
	watcher := &actionWatcher{
		watched: map[uint32]string{42: "http://neverssl.com"},
		started: true,
		stop:    stop,
	}

	watcher.handle(&dbus.Signal{
		Name: notifyInterface + ".NotificationClosed",
		Body: []any{uint32(42), uint32(2)},
	})

	assert.Empty(t, watcher.watched)
	assert.False(t, watcher.started)
	select {
	case <-stop:
	default:
		t.Fatal("watcher did not stop after its final notification closed")
	}
}

func TestActionWatcherInvokesActionAndStops(t *testing.T) {
	origOpen := openPathFunc
	t.Cleanup(func() { openPathFunc = origOpen })

	var opened []string
	openPathFunc = func(path string) { opened = append(opened, path) }

	stop := make(chan struct{})
	watcher := &actionWatcher{
		watched: map[uint32]string{7: "/tmp/report.txt"},
		started: true,
		stop:    stop,
	}

	watcher.handle(&dbus.Signal{
		Name: notifyInterface + ".ActionInvoked",
		Body: []any{uint32(7), "open"},
	})

	assert.Equal(t, []string{"/tmp/report.txt"}, opened)
	assert.Empty(t, watcher.watched)
	assert.False(t, watcher.started)
	select {
	case <-stop:
	default:
		t.Fatal("watcher did not stop after action")
	}
}

func TestActionWatcherFolderActionOpensParent(t *testing.T) {
	origOpen := openPathFunc
	t.Cleanup(func() { openPathFunc = origOpen })

	var opened string
	openPathFunc = func(path string) { opened = path }

	handleAction("folder", filepath.Join("/tmp", "report.txt"))

	assert.Equal(t, "/tmp", opened)
}

func TestActionWatcherIgnoresUnknownNotification(t *testing.T) {
	origOpen := openPathFunc
	t.Cleanup(func() { openPathFunc = origOpen })

	openPathFunc = func(path string) { t.Fatalf("unexpected open: %s", path) }
	stop := make(chan struct{})
	watcher := &actionWatcher{
		watched: map[uint32]string{7: "/tmp/report.txt"},
		started: true,
		stop:    stop,
	}

	watcher.handle(&dbus.Signal{
		Name: notifyInterface + ".ActionInvoked",
		Body: []any{uint32(8), "open"},
	})

	assert.Len(t, watcher.watched, 1)
	assert.True(t, watcher.started)
}
