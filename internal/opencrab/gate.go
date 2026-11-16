package opencrab

// External-gate provisioning for personal librarians (protocol 3,
// opencrab D-1099: the core owns sessions only).
//
// There is no admin plane any more. A gate instance is created by the
// core on the first hello that names it, so "registering" a librarian
// is purely omoikane-side: mint a stable instance id (UUIDv7) for the
// librarian once and keep it on user_librarians. Likewise a /talk
// thread needs no core-side binding — the thread id is the session key
// the gateway sends — so routing a thread through the gateway is just
// the omoikane-side talk_gate_bindings row.
//
// The core still has to know the agent: hello fails with
// subject_unknown until Provision has put the agent row on the runtime,
// which the save flow does first.

import (
	"context"

	"github.com/zenryokukikai/omoikane/internal/gate"
)

// GateProvisioner hands out gate instance ids for personal librarians
// and thread routing ids. One value per process; it holds no state.
type GateProvisioner struct{}

// EnsureInstance returns the librarian's gate instance id, minting one
// when existingInstanceID is empty. Never fails: the id is local.
func (GateProvisioner) EnsureInstance(_ context.Context, _ string, existingInstanceID string) (string, error) {
	if existingInstanceID != "" {
		return existingInstanceID, nil
	}
	return gate.NewUUIDv7(), nil
}

// EnsureThreadBinding returns the routing id recorded for a /talk
// thread (talk_gate_bindings.binding_id). Under protocol 3 it is
// informational — the wire carries the thread id as the session key —
// but the row's existence is what routes the thread through the
// gateway instead of REST dispatch.
func (GateProvisioner) EnsureThreadBinding(_ context.Context, _ string, _ string) (string, error) {
	return gate.NewUUIDv7(), nil
}
