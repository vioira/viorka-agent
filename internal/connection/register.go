package connection

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	managed "github.com/vioira/viorka-agent/internal/runtime"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

// Register accepts single-node and batch tokens. Panel selects the binding flow.
// Existing local identities require explicit replacement through Reenroll.
func (c *Client) Register(ctx context.Context, token, name string) error {
	name = strings.TrimSpace(name)
	if len(token) != 64 || len(name) > 128 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return fmt.Errorf("invalid registration parameters")
	}
	id, err := c.store.Identity(c.cfg.PanelURL, true)
	if err != nil {
		return err
	}
	if id.NodeID != "" {
		return fmt.Errorf("already enrolled; existing identity preserved")
	}
	hostname, err := os.Hostname()
	if err != nil {
		return fmt.Errorf("read hostname: %w", err)
	}
	key := ed25519.PrivateKey(id.PrivateKey).Public().(ed25519.PublicKey)
	engines, err := managed.NewManaged(c.log()).RecoverySnapshots(ctx)
	if err != nil {
		return err
	}
	request := struct {
		Token           string               `json:"token"`
		PublicKey       string               `json:"public_key"`
		ProtocolVersion string               `json:"protocol_version"`
		Name            string               `json:"name,omitempty"`
		Hostname        string               `json:"hostname,omitempty"`
		Engines         []*v1.EngineRecovery `json:"engines,omitempty"`
	}{token, base64.StdEncoding.EncodeToString(key), v1.Version, name, hostname, engines}
	var result struct {
		NodeID          string `json:"node_id"`
		ProtocolVersion string `json:"protocol_version"`
	}
	if err = c.post(ctx, "/api/v1/agents/register", request, &result); err != nil {
		return err
	}
	if result.NodeID == "" || len(result.NodeID) > 128 || result.ProtocolVersion != v1.Version {
		return fmt.Errorf("invalid registration response")
	}
	id.NodeID = result.NodeID
	return c.store.Write("identity.json", id)
}
