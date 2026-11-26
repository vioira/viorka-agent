package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/vioira/viorka-agent/internal/config"
	"github.com/vioira/viorka-agent/internal/network"
	"github.com/vioira/viorka-agent/internal/state"
	agentversion "github.com/vioira/viorka-agent/internal/version"
	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

const installDir = "/root/.viorka-agent"
const journalName = ".agent-update.json"

// Set by the release build from the Ed25519 signing key's public SPKI.
var releasePublicKey string

type receipt struct {
	Task     string         `json:"task"`
	Node     string         `json:"node"`
	Target   v1.AgentUpdate `json:"target"`
	Deadline time.Time      `json:"deadline"`
	Phase    string         `json:"phase"`
}

func (r receipt) save(dir, phase string) error {
	r.Phase = phase
	return state.WriteFile(dir, journalName, r)
}
func readReceipt(dir string) (r receipt, err error) {
	b, err := os.ReadFile(filepath.Join(dir, journalName))
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	err = json.Unmarshal(b, &r)
	if err == nil && (!r.Target.Valid() || r.Task == "" || r.Node == "") {
		err = errors.New("invalid update journal")
	}
	return r, err
}
func publicKey() (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(releasePublicKey)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(b)
	if err != nil {
		return nil, err
	}
	k, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("invalid release key")
	}
	return k, nil
}
func SignedBuild() bool { _, err := publicKey(); return err == nil }
func Managed() bool {
	if runtime.GOOS != "linux" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") || os.Geteuid() != 0 || !SignedBuild() || os.Getenv("VIORKA_SUPERVISED") != "1" {
		return false
	}
	parent, err := os.Readlink("/proc/" + strconv.Itoa(os.Getppid()) + "/exe")
	return err == nil && parent == installDir+"/viorka-agent-supervisor"
}
func CanUpdate(ctx context.Context) bool {
	if !Managed() {
		return false
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	return serviceMatches(bounded)
}
func Ready() error {
	if !Managed() {
		return nil
	}
	return state.WriteFile(installDir, ".agent-ready", os.Getpid())
}
func UpdateReply(node string, d v1.TaskDispatch) (v1.TaskReply, error) {
	if !Managed() {
		return v1.TaskReply{}, errors.New("unsupported installation")
	}
	return updateReply(installDir, node, agentversion.Version, d)
}
func updateReply(dir, node, current string, d v1.TaskDispatch) (v1.TaskReply, error) {
	var reply v1.TaskReply
	if d.Operation != v1.AgentUpdateOperation || d.Engine != nil || d.TrafficCalibration != nil || d.ConfigSHA256 != "" || d.ResourceID != node || d.ResourceKind != "node" || d.Attempt == nil || *d.Attempt == 0 || d.TaskID == "" || len(d.TaskID) > 128 || d.IdempotencyKey == "" || len(d.IdempotencyKey) > 128 || d.AgentUpdate == nil || !d.AgentUpdate.Valid() || d.DeadlineAt.IsZero() || time.Until(d.DeadlineAt) > 11*time.Minute {
		return reply, errors.New("invalid update dispatch")
	}
	reply = v1.TaskReply{TaskID: d.TaskID, Attempt: *d.Attempt, ResourceID: node, State: "accepted"}
	r, err := readReceipt(dir)
	if err != nil {
		return reply, err
	}
	if r.Task == d.TaskID {
		if r.Node != node || r.Target != *d.AgentUpdate || !r.Deadline.Equal(d.DeadlineAt) {
			return reply, errors.New("update identity mismatch")
		}
		switch r.Phase {
		case "pending", "trial", "restoring":
			return reply, nil
		case "healthy":
			if strings.TrimPrefix(current, "v") != strings.TrimPrefix(r.Target.Version, "v") {
				return reply, errors.New("running version differs from receipt")
			}
			reply.State = "succeeded"
		case "update_failed", "update_rolled_back", "update_rollback_failed":
			reply.State = "failed"
			reply.ErrorCode = r.Phase
		default:
			return reply, errors.New("invalid update phase")
		}
		reply.AgentUpdate = d.AgentUpdate
		return reply, nil
	}
	if r.Phase == "pending" || r.Phase == "trial" || r.Phase == "restoring" {
		return reply, errors.New("another update is active")
	}
	if time.Now().After(d.DeadlineAt) || !d.AgentUpdate.NewerThan(current) {
		reply.State = "failed"
		reply.ErrorCode = "update_failed"
		reply.AgentUpdate = d.AgentUpdate
		return reply, nil
	}
	r = receipt{Task: d.TaskID, Node: node, Target: *d.AgentUpdate, Deadline: d.DeadlineAt}
	return reply, r.save(dir, "pending")
}

func download(ctx context.Context, client *http.Client, url string, limit int64, out io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("release asset HTTP %d", res.StatusCode)
	}
	n, err := io.Copy(out, io.LimitReader(res.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("release asset too large")
	}
	return nil
}
func prepare(ctx context.Context, dir string, r receipt) error {
	cfg, err := config.LoadFile(filepath.Join(dir, "agent.json"))
	if err != nil {
		return err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = network.NewDNS(cfg.DNSFallbackServers).DialContext
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: releaseRedirect}
	return prepareAsset(ctx, dir, r, client)
}
func releaseRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.Host != "release-assets.githubusercontent.com" {
		return errors.New("untrusted release redirect")
	}
	return nil
}
func prepareAsset(ctx context.Context, dir string, r receipt, client *http.Client) error {
	key, err := publicKey()
	if err != nil {
		return err
	}
	asset := "viorka-agent-linux-" + runtime.GOARCH
	base := "https://github.com/vioira/viorka-agent/releases/download/" + r.Target.Version + "/" + asset
	var sig strings.Builder
	if err = download(ctx, client, base+".sig", ed25519.SignatureSize, &sig); err != nil {
		return err
	}
	manifest := r.Target.Version + "\n" + asset + "\n" + r.Target.SHA256 + "\n"
	if !ed25519.Verify(key, []byte(manifest), []byte(sig.String())) {
		return errors.New("release signature mismatch")
	}
	// One partial per installation bounds leftover downloads after a power loss.
	partial := filepath.Join(dir, ".agent-next.partial")
	if err = os.Remove(partial); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	if err = download(ctx, client, base, 128<<20, io.MultiWriter(f, h)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != r.Target.SHA256 {
		return errors.New("binary digest mismatch")
	}
	if err = f.Chmod(0700); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	b, err := exec.CommandContext(ctx, f.Name(), "version").Output()
	if err != nil || strings.TrimSpace(string(b)) != "viorka-agent "+strings.TrimPrefix(r.Target.Version, "v")+" (linux/"+runtime.GOARCH+")" {
		return errors.New("candidate version mismatch")
	}
	if err = exec.CommandContext(ctx, f.Name(), "config-check", "--config", filepath.Join(dir, "agent.json")).Run(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, ".agent-next"))
}
