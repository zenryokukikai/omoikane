package opencrab

// External-gate provisioning for personal librarians (issue #104, V3
// contract). The V3 admin surface has no kind/schema registration —
// kind_id is an opaque per-instance value — so provisioning is exactly
// two PUTs: the per-user instance (extends the /my/librarian save
// flow) and the per-/talk-thread binding.
//
// It lives next to the opencrab provisioning client because the two run
// as one pipeline: Provision puts the agent on the runtime, then the
// GateProvisioner registers the same agent as a gate instance on the
// admin plane.

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// GateKindID is omoikane's opaque kind_id on gate instances (V3 §2:
// kind_id references no registry). One instance per personal librarian,
// one binding per /talk thread.
const GateKindID = "omoikane-talk"

// gateInstanceConfig is the canonical (empty) per-instance config: all
// agent identity/config stays omoikane-side. Byte-stable — Instance PUT
// is create-or-byte-equivalent on the decoded config bytes.
var gateInstanceConfig = []byte(`{}`)

// GateInstanceConfigDigest returns the SHA-256 lower-hex digest of the
// canonical instance config bytes — the config_digest every
// omoikane-talk hello must present.
func GateInstanceConfigDigest() string {
	sum := sha256.Sum256(gateInstanceConfig)
	return hex.EncodeToString(sum[:])
}

// ErrSubjectUnresolved is returned by a SubjectResolver that cannot
// (yet) map an omoikane agent to a gate subject_id — the agent row is
// not on the runtime, or the runtime predates the subject_id field.
// Instance registration treats it as "skip for now", not a failure.
var ErrSubjectUnresolved = errors.New("gate subject unresolved")

// SubjectResolver maps an omoikane agent id ("plib-<user_id>") to the
// gate admin plane's positive-i64 subject_id for that agent. The
// production implementation is RuntimeSubjectResolver (subject.go).
type SubjectResolver interface {
	Resolve(ctx context.Context, agentID string) (int64, error)
}

// StubSubjectResolver always answers ErrSubjectUnresolved, which makes
// instance registration a logged no-op. Kept for tests that exercise
// the skip path.
type StubSubjectResolver struct{}

func (StubSubjectResolver) Resolve(context.Context, string) (int64, error) {
	return 0, ErrSubjectUnresolved
}

// GateProvisioner registers per-librarian instances and per-thread
// bindings on the gate admin plane. One value per process.
type GateProvisioner struct {
	Admin    *gate.AdminClient
	Resolver SubjectResolver
	Log      *slog.Logger // nil → slog.Default()
}

func (g *GateProvisioner) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// EnsureInstance makes sure agentID has a gate instance and returns
// its id. existingInstanceID non-empty means a previous save already
// registered one — it is returned as-is (config revisions are not part
// of this flow).
//
// When the resolver answers ErrSubjectUnresolved the registration is
// SKIPPED — logged, ("", nil) returned — so the user-facing save
// succeeds even while the agent has no subject mapping yet (agent row
// not on the runtime, or a runtime without the subject_id field).
func (g *GateProvisioner) EnsureInstance(ctx context.Context, agentID, existingInstanceID string) (string, error) {
	if existingInstanceID != "" {
		return existingInstanceID, nil
	}
	subjectID, err := g.Resolver.Resolve(ctx, agentID)
	if err != nil {
		if errors.Is(err, ErrSubjectUnresolved) {
			g.log().Warn("gate instance registration skipped",
				"agent_id", agentID, "reason", err.Error())
			return "", nil
		}
		return "", err
	}

	instanceID := gate.NewUUIDv7()
	if _, _, err := g.Admin.PutInstance(ctx, instanceID, gate.InstancePut{
		KindID:    GateKindID,
		SubjectID: subjectID,
		Enabled:   true,
		ConfigB64: base64.StdEncoding.EncodeToString(gateInstanceConfig),
	}); err != nil {
		return "", err
	}
	return instanceID, nil
}

// EnsureThreadBinding registers threadID as a gate binding on
// instanceID: a fresh UUIDv7 binding id, address = the thread id (V3
// §5.3: the binding PUT is exactly {instance_id, address}). Dynamic PUT
// is legal while the instance is live (§5.5). Returns the new binding
// id. Callers own the thread-side bookkeeping
// (store.PutTalkGateBinding) and the best-effort policy — this method
// just performs the PUT.
func (g *GateProvisioner) EnsureThreadBinding(ctx context.Context, instanceID, threadID string) (string, error) {
	bindingID := gate.NewUUIDv7()
	if _, _, err := g.Admin.PutBinding(ctx, bindingID, gate.BindingPut{
		InstanceID: instanceID,
		Address:    threadID,
	}); err != nil {
		return "", err
	}
	return bindingID, nil
}
