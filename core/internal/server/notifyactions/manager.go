package notifyactions

import "github.com/AvengeMedia/DankMaterialShell/core/internal/notify"

type Manager struct{}

func NewManager() (*Manager, error) {
	return &Manager{}, nil
}

func (m *Manager) Watch(id uint32, path string) error {
	return notify.WatchAction(id, path)
}

func (m *Manager) Close() {
	notify.CloseActionWatcher()
}
