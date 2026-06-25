package api

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	edgeDNSTokenTTL    = 24 * time.Hour
	edgeDNSTokenHeader = "X-Edge-Access-Token"
)

// edgeDNSToken holds an issued token and its expiry.
type edgeDNSToken struct {
	expiresAt int64
}

// edgeDNSAuth manages AccessKey→Token issuance and validation for the edgeDNSAPI.
type edgeDNSAuth struct {
	keyID     string
	keySecret string

	mu     sync.Mutex
	tokens map[string]*edgeDNSToken
}

func newEdgeDNSAuth(keyID, keySecret string) *edgeDNSAuth {
	return &edgeDNSAuth{
		keyID:     keyID,
		keySecret: keySecret,
		tokens:    make(map[string]*edgeDNSToken),
	}
}

// enabled reports whether edgeDNSAPI auth is configured.
func (a *edgeDNSAuth) enabled() bool {
	return a.keyID != "" && a.keySecret != ""
}

// issueToken creates and stores a new token. Returns token string and expiresAt unix timestamp.
func (a *edgeDNSAuth) issueToken() (string, int64, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", 0, err
	}
	token := hex.EncodeToString(raw)
	expiresAt := time.Now().Add(edgeDNSTokenTTL).Unix()

	a.mu.Lock()
	a.tokens[token] = &edgeDNSToken{expiresAt: expiresAt}
	a.mu.Unlock()

	return token, expiresAt, nil
}

// valid reports whether token exists and has not expired. Expired tokens are purged lazily.
func (a *edgeDNSAuth) valid(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.tokens[token]
	if !ok {
		return false
	}
	if time.Now().Unix() > t.expiresAt {
		delete(a.tokens, token)
		return false
	}
	return true
}

// requireToken is a gin middleware that rejects requests without a valid token.
// It is a no-op when edgeDNSAPI auth is not configured.
func (a *edgeDNSAuth) requireToken() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !a.enabled() {
			c.Next()
			return
		}
		token := c.GetHeader(edgeDNSTokenHeader)
		if token == "" || !a.valid(token) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, edgeDNSErrResp(401, "unauthorized"))
			return
		}
		c.Next()
	}
}

// ── response helpers ──────────────────────────────────────────────────────────

type edgeDNSResponse struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data"`
}

func edgeDNSOK(data any) edgeDNSResponse {
	return edgeDNSResponse{Code: 200, Message: "", Data: data}
}

func edgeDNSErrResp(code int, msg string) edgeDNSResponse {
	return edgeDNSResponse{Code: code, Message: msg, Data: nil}
}
