package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProbeNeverSSLCaptivePortal(t *testing.T) {
	originalURL := portalProbeRequestURL
	originalContent := portalProbeExpectedContent
	t.Cleanup(func() {
		portalProbeRequestURL = originalURL
		portalProbeExpectedContent = originalContent
	})

	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantPortal bool
		wantErr    bool
	}{
		{
			name: "expected response",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<title>NeverSSL - Connecting ... </title>"))
			},
		},
		{
			name: "portal page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html>sign in</html>"))
			},
			wantPortal: true,
		},
		{
			name: "redirect",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://portal.test/login", http.StatusFound)
			},
			wantPortal: true,
		},
		{
			name: "server failure",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			t.Cleanup(server.Close)
			portalProbeRequestURL = server.URL

			portal, err := probeNeverSSLCaptivePortal(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantPortal, portal)
		})
	}
}

func TestManagerPortalProbeMarksCurrentWiFiPortal(t *testing.T) {
	originalProbe := probeCaptivePortal
	t.Cleanup(func() { probeCaptivePortal = originalProbe })
	probeCaptivePortal = func(context.Context) (bool, error) { return true, nil }
	probeContext, cancelProbe := context.WithCancel(context.Background())
	t.Cleanup(cancelProbe)

	backendState := &BackendState{
		NetworkStatus:     StatusWiFi,
		ConnectivityState: nmConnectivityFull,
		WiFiConnected:     true,
		WiFiDevice:        "wlan0",
		WiFiBSSID:         "00:11:22:33:44:55",
		WiFiSSID:          "Guest WiFi",
	}
	manager := &Manager{
		state:              &NetworkState{},
		stateMutex:         sync.RWMutex{},
		portalProbeContext: probeContext,
	}

	detected, shouldProbe := manager.preparePortalProbe(backendState)
	assert.False(t, detected)
	require.True(t, shouldProbe)

	done := make(chan struct{})
	go func() {
		manager.runPortalProbe(portalProbeConnectionKey(backendState))
		close(done)
	}()
	require.Eventually(t, func() bool { return manager.GetState().IsPortal }, time.Second, time.Millisecond)
	cancelProbe()
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)

	detected, shouldProbe = manager.preparePortalProbe(backendState)
	assert.True(t, detected)
	assert.False(t, shouldProbe)
}

func TestManagerPortalProbeResetsForNewWiFi(t *testing.T) {
	manager := &Manager{}
	first := &BackendState{
		NetworkStatus:     StatusWiFi,
		ConnectivityState: nmConnectivityFull,
		WiFiConnected:     true,
		WiFiDevice:        "wlan0",
		WiFiBSSID:         "00:11:22:33:44:55",
		WiFiSSID:          "Guest WiFi",
	}
	second := &BackendState{
		NetworkStatus:     StatusWiFi,
		ConnectivityState: nmConnectivityFull,
		WiFiConnected:     true,
		WiFiDevice:        "wlan0",
		WiFiBSSID:         "66:77:88:99:aa:bb",
		WiFiSSID:          "Other WiFi",
	}

	_, shouldProbe := manager.preparePortalProbe(first)
	require.True(t, shouldProbe)
	manager.portalProbeMu.Lock()
	manager.portalProbeRunning = false
	manager.portalProbeDetected = true
	manager.portalProbeMu.Unlock()

	detected, shouldProbe := manager.preparePortalProbe(second)
	assert.False(t, detected)
	assert.True(t, shouldProbe)
}

func TestManagerPortalProbeDismissesAfterLogin(t *testing.T) {
	originalProbe := probeCaptivePortal
	originalRetry := portalProbeRetryInterval
	t.Cleanup(func() {
		probeCaptivePortal = originalProbe
		portalProbeRetryInterval = originalRetry
	})

	calls := 0
	probeCaptivePortal = func(context.Context) (bool, error) {
		calls++
		return calls == 1, nil
	}
	portalProbeRetryInterval = time.Millisecond

	backendState := &BackendState{
		NetworkStatus:     StatusWiFi,
		ConnectivityState: nmConnectivityFull,
		WiFiConnected:     true,
		WiFiDevice:        "wlan0",
		WiFiBSSID:         "00:11:22:33:44:55",
		WiFiSSID:          "Guest WiFi",
	}
	manager := &Manager{
		state:      &NetworkState{ConnectivityState: nmConnectivityFull},
		stateMutex: sync.RWMutex{},
	}
	_, shouldProbe := manager.preparePortalProbe(backendState)
	require.True(t, shouldProbe)

	manager.runPortalProbe(portalProbeConnectionKey(backendState))
	assert.False(t, manager.GetState().IsPortal)
	assert.Equal(t, 2, calls)
}
