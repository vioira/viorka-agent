package updater

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	v1 "github.com/vioira/viorka-agent/protocol/v1"
)

func testReceipt() receipt {
	return receipt{Task: "task-1", Node: "node-1", Deadline: time.Now().UTC().Add(time.Minute), Target: v1.AgentUpdate{Version: "v2.0.0", SHA256: strings.Repeat("a", 64)}}
}
func TestUpdateReceiptReplay(t *testing.T) {
	dir := t.TempDir()
	r := testReceipt()
	attempt := uint64(1)
	d := v1.TaskDispatch{TaskID: r.Task, Operation: v1.AgentUpdateOperation, AgentUpdate: &r.Target, Attempt: &attempt, ResourceKind: "node", ResourceID: r.Node, DeadlineAt: r.Deadline, IdempotencyKey: "manual-key"}
	for i := 0; i < 2; i++ {
		got, err := updateReply(dir, r.Node, "1.0.0", d)
		if err != nil || got.State != "accepted" {
			t.Fatal(got, err)
		}
	}
	d.AgentUpdate = &v1.AgentUpdate{Version: "v3.0.0", SHA256: r.Target.SHA256}
	if _, err := updateReply(dir, r.Node, "1.0.0", d); err == nil {
		t.Fatal("mismatched target accepted")
	}
	d.AgentUpdate = &r.Target
	for _, phase := range []string{"update_rolled_back", "update_rollback_failed", "healthy"} {
		if err := r.save(dir, phase); err != nil {
			t.Fatal(err)
		}
		got, err := updateReply(dir, r.Node, "2.0.0", d)
		if err != nil || got.AgentUpdate == nil || *got.AgentUpdate != r.Target {
			t.Fatal(got, err)
		}
		if phase == "healthy" && got.State != "succeeded" || phase != "healthy" && (got.State != "failed" || got.ErrorCode != phase) {
			t.Fatal(got)
		}
	}
}
func TestSignedCandidate(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := releasePublicKey
	t.Cleanup(func() { releasePublicKey = oldKey })
	releasePublicKey = base64.StdEncoding.EncodeToString(der)
	r := testReceipt()
	asset := "viorka-agent-linux-" + runtime.GOARCH
	binary := "#!/bin/sh\ncase \"$1\" in version) echo 'viorka-agent 2.0.0 (linux/" + runtime.GOARCH + ")';; config-check) exit 0;; *) exit 1;; esac\n"
	digest := sha256.Sum256([]byte(binary))
	r.Target.SHA256 = hex.EncodeToString(digest[:])
	signature := string(ed25519.Sign(priv, []byte(r.Target.Version+"\n"+asset+"\n"+r.Target.SHA256+"\n")))
	for _, scenario := range []string{"ok", "bad signature", "bad binary"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			client := &http.Client{Transport: roundTrip(func(req *http.Request) (*http.Response, error) {
				b := binary
				if strings.HasSuffix(req.URL.Path, ".sig") {
					b = signature
					if scenario == "bad signature" {
						b = strings.Repeat("x", 64)
					}
				} else if scenario == "bad binary" {
					b += "changed"
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(b))}, nil
			})}
			err := prepareAsset(context.Background(), dir, r, client)
			if (err == nil) != (scenario == "ok") {
				t.Fatal(err)
			}
			_, err = os.Stat(filepath.Join(dir, ".agent-next"))
			if (err == nil) != (scenario == "ok") {
				t.Fatal("unverified file staged", err)
			}
		})
	}
}
func TestActivationRecovery(t *testing.T) {
	for _, scenario := range []string{"healthy", "startup failure", "interrupted trial", "rollback failure"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			r := testReceipt()
			s := supervisor{dir: dir}
			defer func() { s.worker.stop() }()
			healthy := []byte("#!/bin/sh\necho $$ > .agent-ready\nexec sleep 60\n")
			bad := []byte("#!/bin/sh\nexit 1\n")
			current := healthy
			if scenario == "rollback failure" {
				current = bad
			}
			active := filepath.Join(dir, "viorka-agent")
			if err := os.WriteFile(active, current, 0700); err != nil {
				t.Fatal(err)
			}
			next := bad
			if scenario == "healthy" {
				next = healthy
			}
			if err := os.WriteFile(filepath.Join(dir, ".agent-next"), next, 0700); err != nil {
				t.Fatal(err)
			}
			var err error
			if scenario == "interrupted trial" {
				if err = os.Rename(active, active+".previous"); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(filepath.Join(dir, ".agent-next"), active); err != nil {
					t.Fatal(err)
				}
				if err = r.save(dir, "trial"); err != nil {
					t.Fatal(err)
				}
				err = s.recover(ctx)
			} else {
				err = s.activate(ctx, r)
			}
			if (err != nil) != (scenario == "rollback failure") {
				t.Fatal(err)
			}
			got, err := readReceipt(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := "update_rolled_back"
			if scenario == "healthy" {
				want = "healthy"
			} else if scenario == "rollback failure" {
				want = "update_rollback_failed"
			}
			if got.Phase != want {
				t.Fatalf("phase %q, want %q", got.Phase, want)
			}
			b, err := os.ReadFile(active)
			if err != nil || string(b) != string(current) {
				t.Fatal("unexpected installed binary", err)
			}
		})
	}
}
