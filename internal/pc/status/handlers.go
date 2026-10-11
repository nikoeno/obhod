package status

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
)

//go:embed assets
var assets embed.FS

// traffic is the body of GET /api/traffic: totals only, never per-connection hosts.
type traffic struct {
	UploadTotal   int64 `json:"uploadTotal"`
	DownloadTotal int64 `json:"downloadTotal"`
	Connections   int   `json:"connections"`
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.page)
	mux.HandleFunc("/assets/", s.asset)
	mux.HandleFunc("/api/status", s.withToken(s.apiStatus))
	mux.HandleFunc("/api/traffic", s.withToken(s.apiTraffic))
	return s.guard(mux)
}

// guard applies the checks and headers every response gets: the Host header
// (DNS-rebinding guard), GET only, a strict CSP and no CORS.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if !s.hostAllowed(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet {
			h.Set("Allow", http.MethodGet)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed accepts only a loopback IP or "localhost" on our port. A page on
// another origin that rebinds its DNS name to 127.0.0.1 still sends its own
// name in Host and is refused.
func (s *Server) hostAllowed(hostport string) bool {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		host, port = hostport, "80"
		if len(host) > 1 && host[0] == '[' && host[len(host)-1] == ']' {
			host = host[1 : len(host)-1]
		}
	}
	s.mu.Lock()
	want := s.port
	s.mu.Unlock()
	if want != "" && port != want {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}

// withToken requires ?token= when a token is configured.
func (s *Server) withToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Token != "" {
			got := r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.withToken(func(w http.ResponseWriter, r *http.Request) {
		serveAsset(w, "assets/index.html", "text/html; charset=utf-8")
	})(w, r)
}

// asset serves the embedded files; they hold nothing private, so no token is needed.
func (s *Server) asset(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/assets/app.js":
		serveAsset(w, "assets/app.js", "text/javascript; charset=utf-8")
	case "/assets/style.css":
		serveAsset(w, "assets/style.css", "text/css; charset=utf-8")
	default:
		http.NotFound(w, r)
	}
}

func serveAsset(w http.ResponseWriter, name, contentType string) {
	b, err := assets.ReadFile(name)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(b)
}

func (s *Server) apiStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.src.Snapshot())
}

func (s *Server) apiTraffic(w http.ResponseWriter, r *http.Request) {
	if s.live == nil {
		writeJSON(w, http.StatusOK, traffic{})
		return
	}
	snap, err := s.live.Connections(r.Context())
	if err != nil {
		// Details could reveal addresses or secrets; the log of the app has them.
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "traffic data unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, traffic{
		UploadTotal:   snap.UploadTotal,
		DownloadTotal: snap.DownloadTotal,
		Connections:   len(snap.Connections),
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
