package connection

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"github.com/vioira/viorka-agent/internal/state"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

type reenrollment struct {
	TokenHash string         `json:"token_hash"`
	Target    string         `json:"target"`
	Identity  state.Identity `json:"identity"`
}

// Reenroll is an explicit local operation, never a reconnect fallback. The state
// lock excludes a running Agent. Keep the old identity until the new one is bound.
func (c *Client) Reenroll(ctx context.Context, token, node string) error {
	if node == "" || len(node) > 128 || len(token) != 64 {
		return fmt.Errorf("invalid reenrollment parameters")
	}
	old, err := c.store.Identity(c.cfg.PanelURL, false)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil && old.NodeID != "" && old.NodeID != node {
		return fmt.Errorf("existing identity belongs to another node; preserved")
	}
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])
	var pending reenrollment
	err = c.store.Read("reenrollment.json", &pending)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) || pending.TokenHash != hash || pending.Target != node {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		pending = reenrollment{TokenHash: hash, Target: node, Identity: state.Identity{PanelURL: c.cfg.PanelURL, PrivateKey: key}}
		if err = c.store.Write("reenrollment.json", pending); err != nil {
			return err
		}
	}
	if pending.Identity.PanelURL != c.cfg.PanelURL || len(pending.Identity.PrivateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("invalid pending identity")
	}
	key := ed25519.PrivateKey(pending.Identity.PrivateKey).Public().(ed25519.PublicKey)
	var result struct {
		NodeID          string `json:"node_id"`
		ProtocolVersion string `json:"protocol_version"`
	}
	if err = c.post(ctx, "/api/v1/agents/enroll", map[string]string{"token": token, "public_key": base64.StdEncoding.EncodeToString(key), "protocol_version": v1.Version}, &result); err != nil {
		return err
	}
	if result.NodeID != node || result.ProtocolVersion != v1.Version {
		return fmt.Errorf("reenrollment response does not match target node; existing identity preserved")
	}
	pending.Identity.NodeID = node
	// The pending key remains for an exact retry after a lost response or write failure.
	return c.store.Write("identity.json", pending.Identity)
}
