package connection

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/vioira/viorka-agent/internal/state"
)

// The bootstrap origin remains the namespace for local identity and traffic history.
// The verified transport destination is committed after a successful WSS hello.
func (c *Client) origin() string {
	if active := c.activeOrigin.Load(); active != nil {
		return *active
	}
	return c.cfg.PanelURL
}
func (c *Client) credentialOrigin(v sessionCredential) string {
	if v.Origin != "" {
		return v.Origin
	}
	return c.cfg.PanelURL
}
func validMigrationOrigin(value string) bool {
	u, err := url.Parse(value)
	return err == nil && len(value) <= 2048 && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && value == "https://"+u.Host && !strings.ContainsAny(value, "*#")
}
func (c *Client) restoreOrigin(id state.Identity) error {
	if id.ActiveOrigin != "" {
		if !validMigrationOrigin(id.ActiveOrigin) {
			return fmt.Errorf("invalid saved Panel origin")
		}
		c.activeOrigin.Store(&id.ActiveOrigin)
	}
	return nil
}

type panelChallenge struct {
	ID        string    `json:"id"`
	Message   string    `json:"message"`
	ExpiresAt time.Time `json:"expires_at"`
	MigrateTo string    `json:"migrate_to,omitempty"`
}

func (c *Client) issueSession(ctx context.Context, id state.Identity) (sessionCredential, error) {
	if len(id.PrivateKey) != ed25519.PrivateKeySize {
		return sessionCredential{}, fmt.Errorf("invalid identity key")
	}
	trusted := c.origin()
	target := trusted
	var ch panelChallenge
	request := map[string]string{"node_id": id.NodeID}
	if err := c.postAt(ctx, trusted, "/api/v1/agents/challenge", request, &ch); err != nil {
		return sessionCredential{}, err
	}
	if ch.MigrateTo != "" {
		target = ch.MigrateTo
		if !validMigrationOrigin(target) || target == trusted {
			return sessionCredential{}, connectionError("migration", "invalid_target", nil)
		}
		// Get a fresh nonce through the candidate, then redeem it through the last
		// trusted origin. The trusted Panel will reject a nonce issued by a different database.
		ch = panelChallenge{}
		if err := c.postAt(ctx, target, "/api/v1/agents/challenge", request, &ch); err != nil || ch.MigrateTo != "" {
			return sessionCredential{}, connectionError("migration", "target_unavailable", nil)
		}
	}
	expected := fmt.Sprintf("viorka-agent-session-v1\n%s\n%s\n%s\n%s", target, id.NodeID, ch.ID, ch.ExpiresAt.Format(time.RFC3339Nano))
	if ch.Message != expected || !ch.ExpiresAt.After(time.Now()) || ch.ExpiresAt.After(time.Now().Add(2*time.Minute)) {
		return sessionCredential{}, connectionError("authentication", "invalid_challenge", nil)
	}
	sig := ed25519.Sign(ed25519.PrivateKey(id.PrivateKey), []byte(ch.Message))
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := c.postAt(ctx, trusted, "/api/v1/agents/session", map[string]string{"challenge_id": ch.ID, "signature": base64.StdEncoding.EncodeToString(sig)}, &result); err != nil {
		if target != trusted {
			return sessionCredential{}, connectionError("migration", "session_unconfirmed", nil)
		}
		return sessionCredential{}, err
	}
	if len(result.Token) != 64 || !result.ExpiresAt.After(time.Now()) {
		return sessionCredential{}, connectionError("authentication", "invalid_session", nil)
	}
	return sessionCredential{Origin: target, Token: result.Token, ExpiresAt: result.ExpiresAt}, nil
}
