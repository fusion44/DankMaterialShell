package network

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/AvengeMedia/DankMaterialShell/core/internal/log"
)

const (
	portalProbeURL     = "http://neverssl.com"
	portalProbeTimeout = 5 * time.Second
	portalProbeMaxBody = 8 * 1024
	portalProbeRetry   = 15 * time.Second
)

var (
	portalProbeExpectedContent = []byte("<title>NeverSSL - Connecting ... </title>")
	portalProbeRequestURL      = portalProbeURL
	portalProbeRetryInterval   = portalProbeRetry
	probeCaptivePortal         = probeNeverSSLCaptivePortal
)

func probeNeverSSLCaptivePortal(ctx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, portalProbeTimeout)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, portalProbeRequestURL, nil)
	if err != nil {
		return false, fmt.Errorf("create captive portal probe request: %w", err)
	}
	request.Header.Set("User-Agent", "DankMaterialShell/1.0 (Linux)")

	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return false, fmt.Errorf("request captive portal probe: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode >= http.StatusMultipleChoices && response.StatusCode < http.StatusBadRequest {
		return true, nil
	}
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("unexpected captive portal probe status: %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, portalProbeMaxBody))
	if err != nil {
		return false, fmt.Errorf("read captive portal probe response: %w", err)
	}
	return !bytes.Contains(body, portalProbeExpectedContent), nil
}

func portalProbeConnectionKey(state *BackendState) string {
	if state.NetworkStatus != StatusWiFi || !state.WiFiConnected {
		return ""
	}
	return state.WiFiDevice + "\x00" + state.WiFiBSSID + "\x00" + state.WiFiSSID
}

func (m *Manager) preparePortalProbe(state *BackendState) (bool, bool) {
	key := portalProbeConnectionKey(state)

	m.portalProbeMu.Lock()
	defer m.portalProbeMu.Unlock()
	if key != m.portalProbeKey {
		m.portalProbeKey = key
		m.portalProbeCompleted = false
		m.portalProbeDetected = false
	}
	if state.IsPortal {
		m.portalProbeCompleted = true
		return m.portalProbeDetected, false
	}
	if key == "" || state.ConnectivityState != nmConnectivityFull {
		return m.portalProbeDetected, false
	}
	if m.portalProbeCompleted || m.portalProbeRunning {
		return m.portalProbeDetected, false
	}
	m.portalProbeCompleted = true
	m.portalProbeRunning = true
	return false, true
}

func (m *Manager) runPortalProbe(key string) {
	ctx := m.portalProbeContext
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		portal, err := probeCaptivePortal(ctx)
		if err != nil {
			log.Debugf("Captive portal probe failed: %v", err)
		}

		m.portalProbeMu.Lock()
		if key != m.portalProbeKey {
			m.portalProbeMu.Unlock()
			return
		}
		wasDetected := m.portalProbeDetected
		if err != nil {
			if !wasDetected {
				m.portalProbeRunning = false
				m.portalProbeMu.Unlock()
				return
			}
			m.portalProbeMu.Unlock()
			if !waitForPortalProbeRetry(ctx) {
				m.stopPortalProbe(key)
				return
			}
			continue
		}
		if portal {
			m.portalProbeDetected = true
			m.portalProbeMu.Unlock()
			if !wasDetected {
				m.stateMutex.Lock()
				m.state.IsPortal = true
				m.stateMutex.Unlock()
				m.notifySubscribers()
			}
			if !waitForPortalProbeRetry(ctx) {
				m.stopPortalProbe(key)
				return
			}
			continue
		}

		m.portalProbeDetected = false
		m.portalProbeRunning = false
		m.portalProbeMu.Unlock()
		if !wasDetected {
			return
		}
		m.stateMutex.Lock()
		m.state.IsPortal = m.state.ConnectivityState == nmConnectivityPortal
		m.stateMutex.Unlock()
		m.notifySubscribers()
		return
	}
}

func (m *Manager) stopPortalProbe(key string) {
	m.portalProbeMu.Lock()
	defer m.portalProbeMu.Unlock()
	if key == m.portalProbeKey {
		m.portalProbeRunning = false
	}
}

func waitForPortalProbeRetry(ctx context.Context) bool {
	timer := time.NewTimer(portalProbeRetryInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
