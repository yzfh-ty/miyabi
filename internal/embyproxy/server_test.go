package embyproxy

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func availableListen(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func proxyGet(t *testing.T, address string) string {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + address + "/System/Info/Public")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestServerConfigurationPersistsBeforeSwitchAndReleasesPorts(t *testing.T) {
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "first") }))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "second") }))
	defer second.Close()
	server := NewServer(&relayStub{}, "127.0.0.1:8080")
	defer server.Close()
	config := Config{Enabled: true, Listen: availableListen(t), Upstream: first.URL, STRMURL: "http://miyabi:8080"}
	if err := server.Configure(config, nil); err != nil {
		t.Fatal(err)
	}
	if proxyGet(t, config.Listen) != "first" || !server.Status().Running {
		t.Fatal("listener did not start")
	}
	failed := config
	failed.Listen = availableListen(t)
	failed.Upstream = second.URL
	sentinel := errors.New("save failed")
	if err := server.Configure(failed, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("save error: %v", err)
	}
	if proxyGet(t, config.Listen) != "first" || server.Status().Listen != config.Listen {
		t.Fatal("save failure changed active config")
	}
	listener, err := net.Listen("tcp", failed.Listen)
	if err != nil {
		t.Fatalf("failed save leaked new listener: %v", err)
	}
	listener.Close()
	config.Upstream = second.URL
	if err := server.Configure(config, nil); err != nil {
		t.Fatal(err)
	}
	if proxyGet(t, config.Listen) != "second" {
		t.Fatal("same-port reconfiguration retained old upstream")
	}
	old := config.Listen
	config.Listen = availableListen(t)
	if err := server.Configure(config, nil); err != nil {
		t.Fatal(err)
	}
	listener, err = net.Listen("tcp", old)
	if err != nil {
		t.Fatalf("port switch did not release old port: %v", err)
	}
	listener.Close()
	config.Enabled = false
	if err := server.Configure(config, nil); err != nil {
		t.Fatal(err)
	}
	if server.Status().Running {
		t.Fatal("disabled server still running")
	}
	listener, err = net.Listen("tcp", config.Listen)
	if err != nil {
		t.Fatal(err)
	}
	listener.Close()
	config.Enabled = true
	if err := server.Configure(config, nil); err != nil {
		t.Fatal(err)
	}
	server.Close()
	listener, err = net.Listen("tcp", config.Listen)
	if err != nil {
		t.Fatal("shutdown retained port")
	}
	listener.Close()
	if err := server.Configure(config, nil); err == nil {
		t.Fatal("closed service restarted")
	}
}

func TestInvalidOrOccupiedListenCannotSaveSettings(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	for _, address := range []string{"invalid", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:8080", occupied.Addr().String()} {
		t.Run(address, func(t *testing.T) {
			server := NewServer(&relayStub{}, "0.0.0.0:8080")
			defer server.Close()
			persisted := false
			err := server.Configure(Config{Enabled: true, Listen: address, Upstream: "http://emby:8096", STRMURL: "http://miyabi:8080"}, func() error { persisted = true; return nil })
			if err == nil || persisted || server.Status().Running {
				t.Fatal("invalid configuration was saved or started")
			}
		})
	}
	server := NewServer(&relayStub{}, "")
	defer server.Close()
	address := availableListen(t)
	if err := server.Configure(Config{Enabled: true, Listen: address, Upstream: "http://" + address, STRMURL: "http://miyabi:8080"}, nil); err == nil {
		t.Fatal("proxy loop accepted")
	}
	for _, target := range []string{"ftp://emby", "http://secret@emby:8096", "http://emby:8096?token=secret"} {
		if err := server.Configure(Config{Enabled: true, Listen: address, Upstream: target, STRMURL: "http://miyabi:8080"}, nil); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid target accepted or leaked")
		}
	}
}

func TestShutdownClosesUpgradedConnections(t *testing.T) {
	disconnected := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, buffer, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		defer close(disconnected)
		fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
		buffer.Flush()
		buffer.ReadString('\n')
	}))
	defer upstream.Close()
	server := NewServer(&relayStub{}, "")
	defer server.Close()
	address := availableListen(t)
	if err := server.Configure(Config{Enabled: true, Listen: address, Upstream: upstream.URL, STRMURL: "http://miyabi:8080"}, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(3 * time.Second))
	fmt.Fprintf(conn, "GET /emby/websocket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", address)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("upgrade: %v %v", response, err)
	}
	server.Close()
	if _, err := reader.ReadByte(); err == nil {
		t.Fatal("upgraded client retained connection after shutdown")
	}
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("upstream upgraded connection remained open")
	}
}
