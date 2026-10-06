package embyproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const DefaultListen = "0.0.0.0:8099"

type Status struct {
	Running bool
	Listen  string
	Error   string
}

// Server changes listeners only after the settings transaction succeeds.
type Server struct {
	mu        sync.Mutex
	relay     Relay
	reserved  string
	handler   atomic.Pointer[Handler]
	active    *http.Server
	cancel    context.CancelFunc
	listen    string
	lastError string
	closed    bool
}

func NewServer(relay Relay, reservedListen string) *Server {
	return &Server{relay: relay, reserved: reservedListen}
}

// A failed restored listener must not prevent the settings UI from starting.
func (s *Server) Restore(cfg Config) error {
	err := s.Configure(cfg, nil)
	if err != nil {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
	}
	return err
}

func (s *Server) Configure(cfg Config, persist func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("播放反代已关闭")
	}
	var handler *Handler
	var listener net.Listener
	if cfg.Enabled {
		host, port, err := net.SplitHostPort(cfg.Listen)
		number, numberErr := strconv.Atoi(port)
		if err != nil || numberErr != nil || number < 1 || number > 65535 {
			return errors.New("反代监听地址须为主机:端口，端口范围为 1 到 65535")
		}
		_, mainPort, _ := net.SplitHostPort(s.reserved)
		mainNumber, _ := strconv.Atoi(mainPort)
		if mainNumber == number {
			return errors.New("反代端口不能与 Miyabi 主服务端口相同")
		}
		upstream, _ := url.Parse(cfg.Upstream)
		if upstream != nil && upstream.Port() == port && (strings.EqualFold(upstream.Hostname(), "localhost") || upstream.Hostname() == host || net.ParseIP(upstream.Hostname()).IsLoopback() || net.ParseIP(upstream.Hostname()).IsUnspecified()) {
			return errors.New("Emby 上游地址不能指向反代监听端口")
		}
		handler, err = NewHandler(cfg, s.relay)
		if err != nil {
			return err
		}
		if s.active == nil || cfg.Listen != s.listen {
			listener, err = net.Listen("tcp", cfg.Listen)
			if err != nil {
				handler.client.CloseIdleConnections()
				return errors.New("反代端口无法监听，请检查地址或端口占用")
			}
		}
	}
	if persist != nil {
		if err := persist(); err != nil {
			if listener != nil {
				listener.Close()
			}
			if handler != nil {
				handler.client.CloseIdleConnections()
			}
			return err
		}
	}
	previous := s.handler.Swap(handler)
	if previous != nil {
		previous.client.CloseIdleConnections()
	}
	s.lastError = ""
	if !cfg.Enabled || listener != nil {
		if s.active != nil {
			s.cancel()
			s.active.Close()
		}
		s.active, s.listen = nil, ""
	}
	if listener != nil {
		serverContext, cancel := context.WithCancel(context.Background())
		s.cancel = cancel
		server := &http.Server{
			BaseContext: func(net.Listener) context.Context { return serverContext },
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if handler := s.handler.Load(); handler != nil {
					handler.ServeHTTP(w, r)
				} else {
					proxyError(w, http.StatusServiceUnavailable, "播放反代未启用")
				}
			}),
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		s.active, s.listen = server, cfg.Listen
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.mu.Lock()
				if s.active == server {
					cancel()
					s.active, s.listen = nil, ""
					s.lastError = "播放反代监听异常，请重新保存设置"
				}
				s.mu.Unlock()
			}
		}()
	}
	return nil
}

func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Running: s.active != nil, Listen: s.listen, Error: s.lastError}
}

func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.active != nil {
		s.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if s.active.Shutdown(ctx) != nil {
			s.active.Close()
		}
		s.active, s.listen = nil, ""
	}
	if handler := s.handler.Swap(nil); handler != nil {
		handler.client.CloseIdleConnections()
	}
}
