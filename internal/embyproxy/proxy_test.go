package embyproxy

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

type relayStub struct {
	calls             int
	file, ua, address string
	err               error
	probes            int
	headers           http.Header
}

func (s *relayStub) StreamURL(_ context.Context, file, ua string) (string, error) {
	s.calls++
	s.file, s.ua = file, ua
	return s.address, s.err
}

func (s *relayStub) Probe(_ context.Context, _ string, headers http.Header) (*http.Response, error) {
	s.probes++
	s.headers = headers.Clone()
	return &http.Response{StatusCode: 206, Header: http.Header{
		"Content-Type": {"video/mp4"}, "Content-Length": {"123"}, "Accept-Ranges": {"bytes"},
	}, Body: io.NopCloser(strings.NewReader("must not be read"))}, nil
}

func newFixture(t *testing.T, upstream http.HandlerFunc) (*Handler, *relayStub) {
	t.Helper()
	server := httptest.NewServer(upstream)
	t.Cleanup(server.Close)
	relay := &relayStub{address: "https://cdn.example/movie.mp4?sig=keep%2Bthis&expires=123"}
	handler, err := NewHandler(Config{Upstream: server.URL, STRMURL: "http://miyabi:8080", PublicURL: "https://play.example"}, relay)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(handler.client.CloseIdleConnections)
	return handler, relay
}

func playbackBody() string {
	return `{"PlaySessionId":"session","Future":{"keep":true},"MediaSources":[` +
		`{"Id":"mediasource_one","Path":"http://miyabi:8080/api/strm/play/101?token=strm-secret","MediaStreams":[{"Index":2,"Type":"Subtitle"}],"SupportsTranscoding":true,"TranscodingUrl":"/old.m3u8","DirectStreamUrl":"/old?api_key=user-token&DeviceId=device","FutureField":42},` +
		`{"Id":"two","Path":"http://miyabi:8080/api/strm/play/202"},` +
		`{"Id":"local","Path":"/media/video.mkv","TranscodingUrl":"/local.m3u8"}]}`
}

func TestPlaybackInfoPreservesFieldsAndDefersResolution(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			handler, relay := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != method || r.URL.Query().Get("UserId") != "viewer" || r.Header.Get("X-Emby-Token") != "user-token" {
					t.Error("playback request changed")
				}
				if method == http.MethodPost {
					body, _ := io.ReadAll(r.Body)
					if string(body) != `{"DeviceProfile":{"Name":"Keep"}}` {
						t.Errorf("request body changed: %s", body)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ETag", "old")
				io.WriteString(w, playbackBody())
			})
			req := httptest.NewRequest(method, "/emby/iTeMs/7/PlaybackInfo?UserId=viewer", strings.NewReader(`{"DeviceProfile":{"Name":"Keep"}}`))
			req.Header.Set("X-Emby-Token", "user-token")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != 200 || relay.calls != 0 || rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("ETag") != "" {
				t.Fatalf("playback: status=%d calls=%d headers=%v body=%s", rec.Code, relay.calls, rec.Header(), rec.Body.String())
			}
			var data map[string]json.RawMessage
			json.Unmarshal(rec.Body.Bytes(), &data)
			var sources []map[string]json.RawMessage
			json.Unmarshal(data["MediaSources"], &sources)
			if string(data["Future"]) != `{"keep":true}` || string(sources[0]["FutureField"]) != "42" || string(sources[0]["MediaStreams"]) != `[{"Index":2,"Type":"Subtitle"}]` {
				t.Fatal("unknown fields or subtitles lost")
			}
			for _, source := range sources[:2] {
				path, err := url.Parse(stringField(source, "Path"))
				if err != nil || path.Host != "play.example" || path.Path != "/Videos/7/stream" || path.Query().Get("api_key") != "user-token" || path.Query().Get("PlaySessionId") != "session" {
					t.Fatalf("invalid client path: %v %v", path, err)
				}
				if string(source["SupportsTranscoding"]) != "false" || string(source["SupportsDirectPlay"]) != "true" || source["TranscodingUrl"] != nil {
					t.Fatal("115 source can still fall back to transcoding")
				}
			}
			if stringField(sources[2], "Path") != "/media/video.mkv" || stringField(sources[2], "TranscodingUrl") != "/local.m3u8" {
				t.Fatal("local source changed")
			}
		})
	}
}

func TestStreamAuthorizesClientSelectsSourceAndReturns302(t *testing.T) {
	var upstreamCalls atomic.Int32
	handler, relay := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if r.URL.Path != "/Items/7/PlaybackInfo" || r.Method != http.MethodPost || r.Header.Get("X-Emby-Token") != "viewer-token" {
			t.Errorf("unexpected authorization request: %s %s", r.Method, r.URL.Path)
		}
		var input map[string]any
		json.NewDecoder(r.Body).Decode(&input)
		if input["UserId"] != nil || input["EnableTranscoding"] != false || input["IsPlayback"] != false {
			t.Error("untrusted user or playback side effects propagated")
		}
		io.WriteString(w, playbackBody())
	})
	for _, path := range []string{"/Videos/7/stream.mp4", "/emby/VIDEOS/7/original"} {
		req := httptest.NewRequest(http.MethodGet, path+"?mEdIaSoUrCeId=two&API_KEY=viewer-token&UserId=another-user", nil)
		req.Header.Set("User-Agent", "Infuse/7")
		req.Header.Set("Range", "bytes=999-")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != 302 || rec.Header().Get("Location") != relay.address || relay.file != "202" || relay.ua != "Infuse/7" || relay.probes != 0 {
			t.Fatalf("wrong redirect: %d %v relay=%+v", rec.Code, rec.Header(), relay)
		}
	}
	if upstreamCalls.Load() != 2 {
		t.Fatal("authorization was skipped or reused across requests")
	}
}

func TestPlaybackDenialsNeverResolveOrStream(t *testing.T) {
	for _, scenario := range []string{"anonymous", "expired", "forbidden", "error code", "ambiguous", "unknown source", "resolver failure"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			handler, relay := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if !strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
					t.Fatal("denied stream fell back to Emby")
				}
				switch scenario {
				case "expired":
					w.WriteHeader(401)
				case "forbidden":
					w.WriteHeader(403)
				case "error code":
					io.WriteString(w, `{"ErrorCode":"NotAllowed"}`)
				default:
					io.WriteString(w, playbackBody())
				}
			})
			path := "/Videos/7/stream?MediaSourceId=mediasource_one&api_key=viewer"
			want := 403
			switch scenario {
			case "anonymous":
				path = "/Videos/7/stream"
				want = 401
			case "expired":
				want = 401
			case "ambiguous":
				path = "/Videos/7/stream?api_key=viewer"
				want = 400
			case "unknown source":
				path = "/Videos/7/stream?api_key=viewer&MediaSourceId=unknown"
				want = 400
			case "resolver failure":
				relay.err = errors.New("https://cdn.example/?secret=signed")
				want = 502
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			wantCalls := 0
			if scenario == "resolver failure" {
				wantCalls = 1
			}
			if rec.Code != want || relay.calls != wantCalls || strings.Contains(rec.Body.String(), "signed") || rec.Header().Get("Location") != "" {
				t.Fatalf("denial status=%d body=%s relay=%+v", rec.Code, rec.Body.String(), relay)
			}
			if scenario == "anonymous" && calls.Load() != 0 {
				t.Fatal("anonymous request reached upstream")
			}
		})
	}
}

func TestHeadProbesWithoutReadingVideoAndLocalStreamPassesThrough(t *testing.T) {
	handler, relay := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items/7/PlaybackInfo" {
			io.WriteString(w, playbackBody())
			return
		}
		if r.URL.Path != "/Videos/7/stream" || r.Header.Get("Range") != "bytes=5-" || r.URL.Query().Get("StartTimeTicks") != "10" {
			t.Error("ordinary stream request changed")
		}
		w.WriteHeader(206)
		io.WriteString(w, "local-media")
	})
	req := httptest.NewRequest(http.MethodHead, "/Videos/7/stream?api_key=viewer&MediaSourceId=one", nil)
	req.Header.Set("Range", "bytes=1-123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 206 || relay.probes != 1 || relay.headers.Get("Range") != "bytes=1-123" || rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "123" {
		t.Fatalf("HEAD response changed: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/Videos/7/stream?api_key=viewer&MediaSourceId=local&StartTimeTicks=10", nil)
	req.Header.Set("Range", "bytes=5-")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != 206 || rec.Body.String() != "local-media" || relay.calls != 1 {
		t.Fatal("local stream no longer passes through")
	}
}

func TestOnlyMiyabiURLsAreResolved(t *testing.T) {
	handler, _ := newFixture(t, func(http.ResponseWriter, *http.Request) {})
	for _, raw := range []string{"http://foreign/api/strm/play/101", "http://miyabi:8080/not/api/strm/play/101", "http://miyabi:8080/api/strm/play/", "http://miyabi:8080/api/strm/play/..", "https://miyabi:8080/api/strm/play/101"} {
		if handler.fileID(raw) != "" {
			t.Fatalf("accepted foreign/invalid URL %q", raw)
		}
	}
	if handler.fileID("http://miyabi:8080/api/strm/play/101/video.mp4?token=secret") != "101" {
		t.Fatal("filename suffix lost file identity")
	}
}

func TestCompressedPlaybackAndBrowserScriptCompatibility(t *testing.T) {
	for _, path := range []string{"/Items/7/PlaybackInfo", "/web/modules/htmlvideoplayer/basehtmlplayer.js", "/web/modules/htmlvideoplayer/plugin.js"} {
		t.Run(path, func(t *testing.T) {
			handler, _ := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Accept-Encoding") != "identity" || r.Header.Get("If-None-Match") != "" {
					t.Error("conditional/compressed request not normalized")
				}
				w.Header().Set("Content-Encoding", "gzip")
				writer := gzip.NewWriter(w)
				if strings.Contains(path, "PlaybackInfo") {
					io.WriteString(writer, playbackBody())
				} else {
					io.WriteString(writer, `const mode=m.IsRemote&&"DirectPlay"===p?null:"anonymous";hasSubs&&(e.crossOrigin=s);other.crossOrigin="use-credentials";`)
				}
				writer.Close()
			})
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("X-Emby-Token", "viewer")
			req.Header.Set("If-None-Match", "old")
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != 200 || rec.Header().Get("Content-Encoding") != "" {
				t.Fatalf("compressed response: %d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(path, "PlaybackInfo") {
				if !strings.Contains(rec.Body.String(), "play.example") {
					t.Fatal("gzip playback was not rewritten")
				}
			} else if strings.Contains(rec.Body.String(), "anonymous") || strings.Contains(rec.Body.String(), "e.crossOrigin") || !strings.Contains(rec.Body.String(), `other.crossOrigin="use-credentials"`) {
				t.Fatal("browser patch changed unrelated code or retained blocking CORS mode")
			}
		})
	}
}

func TestAuthorizationHeadersAndCookiesAreForwardedWithoutSharedIdentity(t *testing.T) {
	for _, header := range []string{"Authorization", "X-Emby-Authorization", "X-MediaBrowser-Token", "Cookie"} {
		t.Run(header, func(t *testing.T) {
			handler, relay := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if header == "Cookie" {
					if r.Header.Get("Cookie") != "session=viewer" {
						t.Error("cookie lost")
					}
				} else if r.Header.Get("X-Emby-Token") != "viewer" {
					t.Error("client token not passed to Emby")
				}
				io.WriteString(w, playbackBody())
			})
			req := httptest.NewRequest(http.MethodGet, "/Videos/7/stream?MediaSourceId=one", nil)
			value := `Emby Client="Test", Token="viewer"`
			if header == "Cookie" {
				value = "session=viewer"
			} else if header == "X-MediaBrowser-Token" {
				value = "viewer"
			}
			req.Header.Set(header, value)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != 302 || relay.calls != 1 {
				t.Fatalf("%s auth failed: %d", header, rec.Code)
			}
		})
	}
}

func TestUpstreamBasePathIsNotDuplicated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emby/Items/7/PlaybackInfo" {
			t.Errorf("base path duplicated: %s", r.URL.Path)
		}
		io.WriteString(w, playbackBody())
	}))
	defer upstream.Close()
	handler, err := NewHandler(Config{Upstream: upstream.URL + "/emby", STRMURL: "http://miyabi:8080"}, &relayStub{})
	if err != nil {
		t.Fatal(err)
	}
	defer handler.client.CloseIdleConnections()
	for _, path := range []string{"/Items/7/PlaybackInfo", "/emby/Items/7/PlaybackInfo"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Emby-Token", "viewer")
		handler.ServeHTTP(rec, req)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/Videos/7/stream") {
			t.Fatal("base-path response not rewritten")
		}
	}
}

func TestServerDiscoveryKeepsClientsOnProxyAndPreservesIdentity(t *testing.T) {
	handler, _ := newFixture(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"Id":"server-id","ServerName":"Home","LocalAddress":"http://emby:8096","WanAddress":"https://old.example","FutureField":true}`)
	})
	for _, path := range []string{"/System/Info/Public", "/emby/System/Info"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var fields map[string]json.RawMessage
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &fields) != nil {
			t.Fatal("discovery response invalid")
		}
		if stringField(fields, "LocalAddress") != "https://play.example" || stringField(fields, "WanAddress") != "https://play.example" || stringField(fields, "Id") != "server-id" || string(fields["FutureField"]) != "true" {
			t.Fatal("discovery leaked upstream or changed server identity")
		}
	}
}

func TestOrdinaryPostRedirectsAndWebSocketPassThrough(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/emby/websocket" {
			conn, buffer, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			fmt.Fprint(buffer, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			buffer.Flush()
			line, _ := buffer.ReadString('\n')
			fmt.Fprint(buffer, "echo:"+line)
			buffer.Flush()
			return
		}
		if r.URL.Path == "/redirect" {
			w.Header().Set("Location", "http://"+r.Host+"/web/index.html")
			w.WriteHeader(302)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || string(body) != "position=99" || r.Header.Get("Cookie") != "session=viewer" || r.URL.Query().Get("keep") != "a+b" {
			t.Error("control request changed")
		}
		w.Header().Add("Set-Cookie", "session=next; HttpOnly")
		w.WriteHeader(204)
	}))
	defer upstream.Close()
	h, err := NewHandler(Config{Upstream: upstream.URL, STRMURL: "http://miyabi:8080", PublicURL: "https://play.example"}, &relayStub{})
	if err != nil {
		t.Fatal(err)
	}
	defer h.client.CloseIdleConnections()
	req := httptest.NewRequest("POST", "/Sessions/Playing/Progress?keep=a%2Bb", strings.NewReader("position=99"))
	req.Header.Set("Cookie", "session=viewer")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Set-Cookie") != "session=next; HttpOnly" {
		t.Fatal("progress/cookie response changed")
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/redirect", nil))
	if rec.Header().Get("Location") != "https://play.example/web/index.html" {
		t.Fatal("upstream address leaked in redirect")
	}
	proxy := httptest.NewServer(h)
	defer proxy.Close()
	address := strings.TrimPrefix(proxy.URL, "http://")
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET /emby/websocket HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", address)
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: "GET"})
	if err != nil || response.StatusCode != 101 {
		t.Fatalf("websocket upgrade: %v %v", response, err)
	}
	fmt.Fprint(conn, "message\n")
	line, err := reader.ReadString('\n')
	if err != nil || line != "echo:message\n" {
		t.Fatalf("upgrade did not remain bidirectional: %q %v", line, err)
	}
}
