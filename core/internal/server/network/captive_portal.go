package network

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/AvengeMedia/DankMaterialShell/core/internal/log"
	"github.com/AvengeMedia/DankMaterialShell/core/internal/notify"
)

const portalProbeURL = "http://neverssl.com"

var (
	sendActionableNotification = notify.SendActionable
	closeNotification          = notify.Close
	unwatchAction              = notify.UnwatchAction
)

func (m *Manager) ShowCaptivePortalNotification(summary, body, actionLabel string) error {
	summary = sanitizeNotificationText(summary)
	body = sanitizeNotificationText(body)
	actionLabel = sanitizeNotificationText(actionLabel)
	if summary == "" || body == "" || actionLabel == "" {
		return fmt.Errorf("summary, body, and actionLabel are required")
	}

	m.stateMutex.RLock()
	isPortal := m.state.IsPortal
	m.stateMutex.RUnlock()
	if !isPortal {
		return nil
	}

	m.portalNotificationMu.Lock()
	defer m.portalNotificationMu.Unlock()
	if m.portalNotificationID != 0 {
		return nil
	}
	m.stateMutex.RLock()
	isPortal = m.state.IsPortal
	m.stateMutex.RUnlock()
	if !isPortal {
		return nil
	}

	id, err := sendActionableNotification(notify.Notification{
		Summary:      summary,
		Body:         body,
		Icon:         "network-wireless",
		ActionTarget: portalProbeURL,
		OpenLabel:    actionLabel,
		Persistent:   true,
	})
	if err != nil {
		return fmt.Errorf("send captive portal notification: %w", err)
	}
	m.portalNotificationID = id
	return nil
}

var notificationTextReplacer = strings.NewReplacer(
	"<", "‹", ">", "›", "&", "＆",
	"[", "［", "]", "］", "(", "（", ")", "）",
	"*", "＊", "_", "＿", "`", "｀", "~", "～", "#", "＃", ":", "꞉", "\\", "＼",
)

func sanitizeNotificationText(text string) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
	return strings.TrimSpace(notificationTextReplacer.Replace(text))
}

func (m *Manager) DismissCaptivePortalNotification() {
	id := m.takeCaptivePortalNotification()
	if id == 0 {
		return
	}
	m.closeCaptivePortalNotification(id)
}

func (m *Manager) takeCaptivePortalNotification() uint32 {
	m.portalNotificationMu.Lock()
	defer m.portalNotificationMu.Unlock()
	id := m.portalNotificationID
	m.portalNotificationID = 0
	return id
}

func (m *Manager) closeCaptivePortalNotification(id uint32) {
	unwatchAction(id)
	if err := closeNotification(id); err != nil {
		log.Warnf("Failed to dismiss captive portal notification: %v", err)
	}
}
