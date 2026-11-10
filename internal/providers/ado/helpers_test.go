package ado

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/MichalOndrejka/conduit/internal/models"
	"github.com/MichalOndrejka/conduit/internal/providers"
)

func init() {
	providers.RetryBaseDelay = time.Millisecond
}

func testConn(baseURL, token string) providers.Conn {
	return providers.Conn{Provider: models.Provider{ID: "p1", Kind: Kind, BaseURL: baseURL}, Token: token}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
