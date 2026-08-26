package opencrab

// External-gate provisioning for personal librarians (issue #104 slice
// G2). This file owns the omoikane-talk kind/schema bootstrap payloads
// and the per-user instance registration that extends the /my/librarian
// save flow. Binding creation (per /talk thread) is slice G3.
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
	"sync"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// GateKindID is omoikane's external gate kind. One instance per
// personal librarian, one binding per /talk thread.
const GateKindID = "omoikane-talk"

const (
	gateConfigSchemaID = "omoikane-talk-config"
	gateSecretSchemaID = "omoikane-talk-secrets"
)

// GateThreadAddressForm matches omoikane /talk thread ids as binding
// addresses: store.newLibrarianID("thread") = "thread-" + 8 lower hex.
// Exported because the same value is both the kind registration's
// address_form (here) and the hello frame's address_form (the
// omoikane-gate binary) — one contract, one constant.
const GateThreadAddressForm = "^thread-[0-9a-f]{8}$"

// GateOriginScope is the omoikane-talk kind's origin scope: origins
// (librarian_chat message ids) are unique per instance. Shared by the
// kind registration and the hello frame for the same reason as
// GateThreadAddressForm.
const GateOriginScope = "instance"

// GateInstanceConfigDigest returns the SHA-256 hex digest of the
// canonical (empty) instance config — the config_digest every
// omoikane-talk hello must present, since every instance is registered
// with the same gateInstanceConfig bytes.
func GateInstanceConfigDigest() string {
	sum := sha256.Sum256(gateInstanceConfig)
	return hex.EncodeToString(sum[:])
}

// Bootstrap documents. Byte-stable literals: PUT gate-schemas is
// create-or-byte-equivalent, so these must serialize identically on
// every process start.
//
//   - config schema: the omoikane-talk instance config is the empty
//     object (all agent identity/config stays omoikane-side), expressed
//     as a minimal JSON Schema 2020-12.
//   - secret manifest: no gate-held secrets (issue #104 裁定A — the kb
//     token is fetched omoikane-internally, never deposited with the
//     gate), so required and optional are both empty.
var (
	gateConfigSchemaDoc = []byte(`{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","additionalProperties":false}`)
	gateSecretSchemaDoc = []byte(`{"required":[],"optional":[]}`)

	// gateInstanceConfig is the canonical (empty) per-instance config.
	gateInstanceConfig = []byte(`{}`)

	// gateBindingMetadata is the canonical (empty) per-binding
	// metadata: the omoikane-talk kind declares no binding metadata
	// schema, so every binding carries the empty object.
	gateBindingMetadata = []byte(`{}`)
)

// ErrSubjectUnresolved is returned by a SubjectResolver that cannot
// yet map an omoikane agent to a gate subject_id. Instance
// registration treats it as "skip for now", not a failure.
var ErrSubjectUnresolved = errors.New("gate subject resolver not implemented: waiting on upstream opencrab#763")

// SubjectResolver maps an omoikane agent id ("plib-<user_id>") to the
// gate admin plane's positive-i64 subject_id for that agent.
type SubjectResolver interface {
	Resolve(ctx context.Context, agentID string) (int64, error)
}

// StubSubjectResolver is the placeholder until the admin plane exposes
// a subject lookup (upstream opencrab#763). It always answers
// ErrSubjectUnresolved, which makes instance registration a logged
// no-op while everything around it stays wired and testable.
type StubSubjectResolver struct{}

func (StubSubjectResolver) Resolve(context.Context, string) (int64, error) {
	return 0, ErrSubjectUnresolved
}

// GateProvisioner registers omoikane's kind/schemas and per-librarian
// instances on the gate admin plane. One value per process.
type GateProvisioner struct {
	Admin    *gate.AdminClient
	Resolver SubjectResolver
	Log      *slog.Logger // nil → slog.Default()

	// Registration latch: once-per-process AFTER a success. A plain
	// sync.Once would also latch a failure (one admin-plane blip at
	// first save would disable registration until restart), so this is
	// a mutex + success flag instead.
	mu         sync.Mutex
	registered bool
}

func (g *GateProvisioner) log() *slog.Logger {
	if g.Log != nil {
		return g.Log
	}
	return slog.Default()
}

// EnsureRegistered PUTs the omoikane-talk schemas then the kind.
// Idempotent per the admin contract (PUT is create-or-equivalent) and
// latched process-wide after the first success.
func (g *GateProvisioner) EnsureRegistered(ctx context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.registered {
		return nil
	}

	if _, _, err := g.Admin.PutSchema(ctx, gateConfigSchemaID, gate.SchemaPut{
		Role:        "instance_config",
		Format:      "json-schema-2020-12",
		DocumentB64: base64.StdEncoding.EncodeToString(gateConfigSchemaDoc),
	}); err != nil {
		return err
	}
	if _, _, err := g.Admin.PutSchema(ctx, gateSecretSchemaID, gate.SchemaPut{
		Role:        "secret_manifest",
		Format:      "secret-manifest-v1",
		DocumentB64: base64.StdEncoding.EncodeToString(gateSecretSchemaDoc),
	}); err != nil {
		return err
	}

	addressForm := GateThreadAddressForm
	configSchema := gateConfigSchemaID
	secretSchema := gateSecretSchemaID
	catchUp := "none"
	if _, _, err := g.Admin.PutKind(ctx, GateKindID, gate.KindPut{
		ProtocolMajor:           2,
		OriginScope:             GateOriginScope,
		IngressDiscovery:        "prebound",
		AddressForm:             &addressForm,
		ConfigSchemaID:          &configSchema,
		BindingMetadataSchemaID: nil,
		SecretManifestSchemaID:  &secretSchema,
		CatchUpMode:             &catchUp,
		CursorSchemaID:          nil,
	}); err != nil {
		return err
	}

	g.registered = true
	return nil
}

// EnsureInstance makes sure agentID has a gate instance and returns
// its id. existingInstanceID non-empty means a previous save already
// registered one — it is returned as-is (the aggregate is immutable
// for our purposes; config revisions are not part of this flow).
//
// When the resolver answers ErrSubjectUnresolved the registration is
// SKIPPED — logged, ("", nil) returned — so the user-facing save
// succeeds while the subject lookup is still upstream work.
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
		Label:     agentID,
		SubjectID: subjectID,
		Enabled:   true,
		ConfigB64: base64.StdEncoding.EncodeToString(gateInstanceConfig),
	}); err != nil {
		return "", err
	}
	return instanceID, nil
}

// EnsureThreadBinding registers threadID as a gate binding on
// instanceID (issue #104 G3a): a fresh UUIDv7 binding id, address =
// the thread id, no catch-up start (the kind's catch_up_mode is
// "none") and the empty metadata object (the kind declares no binding
// metadata schema). Returns the new binding id. Callers own the
// thread-side bookkeeping (store.PutTalkGateBinding) and the
// best-effort policy — this method just performs the PUT.
func (g *GateProvisioner) EnsureThreadBinding(ctx context.Context, instanceID, threadID string) (string, error) {
	bindingID := gate.NewUUIDv7()
	if _, _, err := g.Admin.PutBinding(ctx, bindingID, gate.BindingPut{
		InstanceID:         instanceID,
		Address:            threadID,
		Label:              nil,
		BindingMetadataB64: base64.StdEncoding.EncodeToString(gateBindingMetadata),
		CatchUpStart:       nil,
	}); err != nil {
		return "", err
	}
	return bindingID, nil
}
