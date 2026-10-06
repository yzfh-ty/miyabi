// Package embyproxy redirects Miyabi STRM playback after Emby authorizes the client.
package embyproxy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 8 << 20

type Relay interface {
	StreamURL(context.Context, string, string) (string, error)
	Probe(context.Context, string, http.Header) (*http.Response, error)
}

type Config struct {
	Enabled   bool
	Listen    string
	Upstream  string
	STRMURL   string
	PublicURL string
}

type Handler struct {
	cfg      Config
	upstream *url.URL
	strmURL  *url.URL
	relay    Relay
	client   *http.Client
	proxy    *httputil.ReverseProxy
}

var (
	playbackRoute = regexp.MustCompile(`(?i)^(?:/emby)?/items/([^/]+)/playbackinfo$`)
	streamRoute   = regexp.MustCompile(`(?i)^(?:/emby)?/videos/([^/]+)/(?:stream|original)(?:\.[a-z0-9]+)?$`)
	authToken     = regexp.MustCompile(`(?i)\bToken\s*=\s*(?:"([^"]+)"|([^,\s]+))`)
)

func NewHandler(cfg Config, relay Relay) (*Handler, error) {
	target, err := parseHTTPURL(cfg.Upstream)
	if err != nil {
		return nil, errors.New("Emby 上游地址必须是有效的 HTTP 或 HTTPS 地址")
	}
	strmURL, err := parseHTTPURL(cfg.STRMURL)
	if err != nil {
		return nil, errors.New("请先配置 Miyabi STRM 服务地址")
	}
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	if cfg.PublicURL != "" {
		public, err := parseHTTPURL(cfg.PublicURL)
		if err != nil || (public.Path != "" && public.Path != "/") {
			return nil, errors.New("反代访问地址必须是 HTTP 或 HTTPS 根地址")
		}
		if strings.EqualFold(public.Host, target.Host) {
			return nil, errors.New("反代访问地址不能与 Emby 上游地址相同")
		}
	}
	if relay == nil {
		return nil, errors.New("115 播放解析器未初始化")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	transport.ResponseHeaderTimeout = 15 * time.Second
	h := &Handler{cfg: cfg, upstream: target, strmURL: strmURL, relay: relay, client: &http.Client{
		Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
	h.proxy = &httputil.ReverseProxy{
		Transport: transport,
		Rewrite: func(r *httputil.ProxyRequest) {
			// Both /Items/... and /emby/Items/... work with a configured /emby base.
			if base := strings.TrimRight(target.Path, "/"); base != "" && strings.HasPrefix(r.Out.URL.Path, base+"/") {
				r.Out.URL.Path = strings.TrimPrefix(r.Out.URL.Path, base)
				r.Out.URL.RawPath = ""
			}
			r.SetURL(target)
			r.SetXForwarded()
			if playbackRoute.MatchString(r.In.URL.Path) || browserPlayer(r.In.URL.Path) {
				r.Out.Header.Set("Accept-Encoding", "identity")
				r.Out.Header.Del("If-None-Match")
				r.Out.Header.Del("If-Modified-Since")
			}
		},
		ModifyResponse: h.modifyResponse,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			proxyError(w, http.StatusBadGateway, "Emby 反代请求失败")
		},
	}
	return h, nil
}

func parseHTTPURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid HTTP URL")
	}
	return u, nil
}

type requestOrigin struct{}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.EqualFold(r.Host, h.upstream.Host) {
		proxyError(w, http.StatusBadGateway, "Emby 上游地址指向了反代入口")
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), requestOrigin{}, h.origin(r)))
	if match := streamRoute.FindStringSubmatch(r.URL.Path); match != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		h.serveStream(w, r, match[1])
		return
	}
	h.proxy.ServeHTTP(w, r)
}

func (h *Handler) origin(r *http.Request) string {
	if h.cfg.PublicURL != "" {
		return strings.TrimRight(h.cfg.PublicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handler) fileID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	base := h.strmURL
	if !strings.EqualFold(u.Host, base.Host) || u.Scheme != base.Scheme {
		return ""
	}
	prefix := strings.TrimRight(base.Path, "/") + "/api/strm/play/"
	if !strings.HasPrefix(u.Path, prefix) {
		return ""
	}
	id := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")[0]
	if id == "" || len(id) > 128 || strings.ContainsAny(id, " .\\") {
		return ""
	}
	return id
}

func queryValue(values url.Values, name string) string {
	for key, items := range values {
		if strings.EqualFold(key, name) && len(items) > 0 {
			return items[0]
		}
	}
	return ""
}

func clientToken(r *http.Request) string {
	for _, name := range []string{"api_key", "X-Emby-Token"} {
		if token := queryValue(r.URL.Query(), name); token != "" {
			return token
		}
	}
	for _, name := range []string{"X-Emby-Token", "X-MediaBrowser-Token"} {
		if token := r.Header.Get(name); token != "" {
			return token
		}
	}
	for _, name := range []string{"Authorization", "X-Emby-Authorization"} {
		if match := authToken.FindStringSubmatch(r.Header.Get(name)); match != nil {
			return match[1] + match[2]
		}
	}
	return ""
}

func (h *Handler) playbackInfo(r *http.Request, itemID string, result any) (int, error) {
	target := *h.upstream
	target.Path = strings.TrimRight(target.Path, "/") + "/Items/" + itemID + "/PlaybackInfo"
	target.RawPath = ""
	input := strings.NewReader(`{"EnableDirectPlay":true,"EnableDirectStream":true,"EnableTranscoding":false,"AutoOpenLiveStream":false,"IsPlayback":false}`)
	request, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), input)
	if err != nil {
		return http.StatusBadGateway, err
	}
	// Never inject the application's Emby management API key.
	for _, name := range []string{"Authorization", "X-Emby-Authorization", "Cookie", "User-Agent", "X-Emby-Token", "X-MediaBrowser-Token", "X-Emby-Device-Id"} {
		if value := r.Header.Get(name); value != "" {
			request.Header.Set(name, value)
		}
	}
	if token := clientToken(r); token != "" {
		request.Header.Set("X-Emby-Token", token)
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Content-Type", "application/json")
	response, err := h.client.Do(request)
	if err != nil {
		return http.StatusBadGateway, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusNotFound {
			return response.StatusCode, errors.New("Emby rejected playback")
		}
		return http.StatusBadGateway, errors.New("unexpected Emby response")
	}
	body, err := readHTTPBody(response)
	if err == nil {
		err = json.Unmarshal(body, result)
	}
	return http.StatusBadGateway, err
}

func (h *Handler) serveStream(w http.ResponseWriter, r *http.Request, itemID string) {
	if clientToken(r) == "" && r.Header.Get("Authorization") == "" && r.Header.Get("X-Emby-Authorization") == "" && r.Header.Get("Cookie") == "" {
		proxyError(w, http.StatusUnauthorized, "请先登录 Emby")
		return
	}
	var item struct {
		Sources   []map[string]json.RawMessage `json:"MediaSources"`
		ErrorCode string                       `json:"ErrorCode"`
	}
	status, err := h.playbackInfo(r, itemID, &item)
	if err != nil {
		proxyError(w, status, "无法访问该 Emby 影片")
		return
	}
	if item.ErrorCode != "" && item.ErrorCode != "None" {
		proxyError(w, http.StatusForbidden, "Emby 未允许播放该影片")
		return
	}
	if len(item.Sources) == 0 {
		proxyError(w, http.StatusBadGateway, "Emby 未返回媒体源")
		return
	}
	requested := queryValue(r.URL.Query(), "MediaSourceId")
	var selected map[string]json.RawMessage
	matched := false
	for _, source := range item.Sources {
		if h.fileID(stringField(source, "Path")) != "" {
			matched = true
		}
		if requested != "" && sourceID(stringField(source, "Id")) == sourceID(requested) {
			selected = source
		}
	}
	if requested == "" && len(item.Sources) == 1 {
		selected = item.Sources[0]
	}
	if selected == nil && matched {
		proxyError(w, http.StatusBadRequest, "请指定有效的 Emby 媒体源")
		return
	}
	id := h.fileID(stringField(selected, "Path"))
	if id == "" {
		h.proxy.ServeHTTP(w, r)
		return
	}
	address, err := h.relay.StreamURL(r.Context(), id, r.UserAgent())
	if err != nil {
		proxyError(w, http.StatusBadGateway, "115 播放地址获取失败，请重试")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		response, err := h.relay.Probe(r.Context(), address, r.Header)
		if err != nil {
			proxyError(w, http.StatusBadGateway, "115 媒体探测失败")
			return
		}
		defer response.Body.Close()
		for _, name := range []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "ETag", "Last-Modified"} {
			if value := response.Header.Get(name); value != "" {
				w.Header().Set(name, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		return
	}
	http.Redirect(w, r, address, http.StatusFound)
}

func sourceID(id string) string { return strings.TrimPrefix(id, "mediasource_") }

func stringField(fields map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return value
}

func readResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponseBytes+1))
	if len(body) > maxResponseBytes {
		return nil, errors.New("Emby response too large")
	}
	return body, err
}

func readHTTPBody(response *http.Response) ([]byte, error) {
	if response.Header.Get("Content-Encoding") == "gzip" {
		reader, err := gzip.NewReader(response.Body)
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		return readResponse(reader)
	}
	if encoding := response.Header.Get("Content-Encoding"); encoding != "" && encoding != "identity" {
		return nil, errors.New("unsupported Emby response encoding")
	}
	return readResponse(response.Body)
}

func proxyError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, message, status)
}

func (h *Handler) modifyResponse(response *http.Response) error {
	origin, _ := response.Request.Context().Value(requestOrigin{}).(string)
	if location := response.Header.Get("Location"); location != "" {
		u, err := url.Parse(location)
		if err == nil && strings.EqualFold(u.Host, h.upstream.Host) {
			public, _ := url.Parse(origin)
			u.Scheme, u.Host = public.Scheme, public.Host
			response.Header.Set("Location", u.String())
		}
	}
	if response.StatusCode != http.StatusOK || response.Request.Method == http.MethodHead {
		return nil
	}
	// The proxy may have joined an upstream /emby base path.
	requestPath := response.Request.URL.Path
	if playbackRoute.MatchString(requestPath) {
		return h.modifyPlayback(response, origin)
	}
	if path := strings.ToLower(requestPath); strings.HasSuffix(path, "/system/info") || strings.HasSuffix(path, "/system/info/public") {
		body, err := readHTTPBody(response)
		response.Body.Close()
		if err != nil {
			return err
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(body, &fields) == nil {
			for _, name := range []string{"LocalAddress", "WanAddress"} {
				if _, present := fields[name]; present {
					fields[name], _ = json.Marshal(origin)
				}
			}
			body, err = json.Marshal(fields)
			if err != nil {
				return err
			}
		}
		setResponseBody(response, body, true)
	}
	if browserPlayer(requestPath) {
		body, err := readHTTPBody(response)
		response.Body.Close()
		if err != nil {
			return err
		}
		// Emby sets anonymous CORS on remote direct streams. Redirected 115
		// downloads need the ordinary, non-CORS media element loading mode.
		body = corsMode.ReplaceAll(body, []byte(`null`))
		body = corsAssignment.ReplaceAll(body, nil)
		setResponseBody(response, body, false)
		response.Header.Set("Cache-Control", "no-cache")
	}
	return nil
}

func browserPlayer(path string) bool {
	path = strings.ToLower(path)
	return strings.HasSuffix(path, "/web/modules/htmlvideoplayer/basehtmlplayer.js") || strings.HasSuffix(path, "/web/modules/htmlvideoplayer/plugin.js")
}

var (
	corsMode       = regexp.MustCompile(`\w+\.IsRemote\s*&&\s*"DirectPlay"\s*===\s*\w+\s*\?\s*null\s*:\s*"anonymous"`)
	corsAssignment = regexp.MustCompile(`&&\s*\(\w+\.crossOrigin\s*=\s*\w+\)`)
)

func (h *Handler) modifyPlayback(response *http.Response, origin string) error {
	body, err := readHTTPBody(response)
	response.Body.Close()
	if err != nil {
		return err
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(body, &data); err != nil {
		return err
	}
	var sources []map[string]json.RawMessage
	if err := json.Unmarshal(data["MediaSources"], &sources); err != nil {
		setResponseBody(response, body, false)
		return nil
	}
	match := playbackRoute.FindStringSubmatch(response.Request.URL.Path)
	changed := false
	for _, source := range sources {
		if h.fileID(stringField(source, "Path")) == "" {
			continue
		}
		query := url.Values{"Static": {"true"}, "MediaSourceId": {stringField(source, "Id")}}
		if session := stringField(data, "PlaySessionId"); session != "" {
			query.Set("PlaySessionId", session)
		}
		if previous, err := url.Parse(stringField(source, "DirectStreamUrl")); err == nil {
			for _, name := range []string{"api_key", "X-Emby-Token", "DeviceId"} {
				if value := queryValue(previous.Query(), name); value != "" {
					query.Set(name, value)
				}
			}
		}
		if token := clientToken(response.Request); token != "" {
			query.Del("X-Emby-Token")
			query.Set("api_key", token)
		}
		endpoint := "/Videos/" + url.PathEscape(match[1]) + "/stream?" + query.Encode()
		source["DirectStreamUrl"], _ = json.Marshal(endpoint)
		source["Path"], _ = json.Marshal(origin + endpoint)
		source["SupportsDirectPlay"] = json.RawMessage("true")
		source["SupportsDirectStream"] = json.RawMessage("true")
		source["SupportsTranscoding"] = json.RawMessage("false")
		for _, name := range []string{"TranscodingUrl", "TranscodingContainer", "TranscodingSubProtocol"} {
			delete(source, name)
		}
		changed = true
	}
	if changed {
		data["MediaSources"], _ = json.Marshal(sources)
		body, err = json.Marshal(data)
		if err != nil {
			return err
		}
	}
	setResponseBody(response, body, changed)
	return nil
}

func setResponseBody(response *http.Response, body []byte, private bool) {
	response.Body = io.NopCloser(bytes.NewReader(body))
	response.ContentLength = int64(len(body))
	response.Header.Set("Content-Length", strconv.Itoa(len(body)))
	response.Header.Del("Content-Encoding")
	response.Header.Del("ETag")
	response.Header.Del("Last-Modified")
	if private {
		response.Header.Set("Cache-Control", "no-store")
	}
}
