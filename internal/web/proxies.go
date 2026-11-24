package web

// proxies.go exposes the active outbound proxy pool to the task proxy picker.
// The view is masked (Proxy.String never prints credentials) and read-only:
// management stays in Settings › Proxy (hot-applied).

import (
	"net/http"

	"github.com/c1ayooo/xalgorix/v4/internal/proxy"
)

type proxyListView struct {
	Enabled  bool              `json:"enabled"`
	Required bool              `json:"required"`
	Entries  []proxy.ScanEntry `json:"entries"`
}

// handleProxyList serves GET /api/proxies.
func (s *Server) handleProxyList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	entries := proxy.Entries()
	if entries == nil {
		entries = []proxy.ScanEntry{}
	}
	writeJSONStatus(w, http.StatusOK, proxyListView{
		Enabled:  proxy.Enabled(),
		Required: proxy.Required(),
		Entries:  entries,
	})
}
