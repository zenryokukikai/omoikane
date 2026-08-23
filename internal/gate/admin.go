package gate

// Admin-plane client for the opencrab external gate registration API
// (issue #104 slice G2). The authoritative contract is opencrab
// docs/design/external-gate.md §3 (admin wire 正本): exact DTO field
// order, the uniform error envelope, and the total HTTP-status→code
// map. This file covers the 11 operations omoikane's provisioning
// needs (schemas, kinds, instances, revisions, bindings); the
// source-cursor / catch-up operations are out of scope while the
// omoikane-talk kind runs catch_up_mode=none.
//
// DTO structs are declared in the spec's §3.1 member order — Go's
// encoding/json serializes struct fields in declaration order, which
// is how the "object field order is as written" requirement is met.
// Nullable members use pointers WITHOUT omitempty: the spec requires
// the member present with a null value, never absent.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ---- error model ----------------------------------------------------

// AdminError is the uniform admin error envelope
// {"error":{code,at,detail}} plus the HTTP status it arrived with.
// At/Detail mirror the wire: always present, possibly null.
type AdminError struct {
	Status int
	Code   string
	At     *string
	Detail *string
}

func (e *AdminError) Error() string {
	msg := fmt.Sprintf("gate admin: %s (HTTP %d)", e.Code, e.Status)
	if e.At != nil {
		msg += " at " + *e.At
	}
	if e.Detail != nil {
		msg += ": " + *e.Detail
	}
	return msg
}

// adminStatusCodes is the spec §3 total map: every stable error code
// and the one HTTP status it may arrive with. Used to fail loudly on
// an envelope that contradicts the contract.
var adminStatusCodes = map[string]int{
	"unauthorized":              401,
	"bad_request":               400,
	"bad_schema_id":             400,
	"bad_kind_id":               400,
	"bad_instance_id":           400,
	"bad_binding_id":            400,
	"unknown_field":             400,
	"path_body_mismatch":        400,
	"schema_unknown":            404,
	"kind_unknown":              404,
	"subject_unknown":           404,
	"instance_unknown":          404,
	"binding_unknown":           404,
	"schema_conflict":           409,
	"kind_in_use":               409,
	"builtin_reserved":          409,
	"instance_conflict":         409,
	"instance_deleted":          409,
	"revision_conflict":         409,
	"instance_disabled":         409,
	"binding_closed":            409,
	"binding_conflict":          409,
	"address_in_use":            409,
	"instance_active":           409,
	"instance_not_ready":        409,
	"epoch_mismatch":            409,
	"catch_up_in_progress":      409,
	"schema_validation_failed":  422,
	"schema_role_mismatch":      422,
	"address_form_invalid":      422,
	"address_invalid":           422,
	"catch_up_contract_invalid": 422,
	"cursor_invalid":            422,
	"catch_up_unsupported":      422,
	"store_error":               500,
}

// ---- DTOs (spec §3.1, declaration order = wire order) ---------------

// Schema is the stored schema document row.
type Schema struct {
	SchemaID       string `json:"schema_id"`
	Role           string `json:"role"`
	Format         string `json:"format"`
	DocumentB64    string `json:"document_b64"`
	DocumentDigest string `json:"document_digest"`
	CreatedAt      int64  `json:"created_at"`
}

// Kind is a registered gate kind.
type Kind struct {
	KindID                  string  `json:"kind_id"`
	Registration            string  `json:"registration"`
	ProtocolMajor           uint32  `json:"protocol_major"`
	OriginScope             string  `json:"origin_scope"`
	IngressDiscovery        string  `json:"ingress_discovery"`
	AddressForm             *string `json:"address_form"`
	ConfigSchemaID          *string `json:"config_schema_id"`
	BindingMetadataSchemaID *string `json:"binding_metadata_schema_id"`
	SecretManifestSchemaID  *string `json:"secret_manifest_schema_id"`
	CatchUpMode             *string `json:"catch_up_mode"`
	CursorSchemaID          *string `json:"cursor_schema_id"`
}

// Connection is the readiness projection inside an Instance response.
type Connection struct {
	State    string  `json:"state"`
	Revision *uint64 `json:"revision"`
	Epoch    *uint64 `json:"epoch"`
}

// SecretManifest lists the kind's secret names (byte-sorted, distinct).
type SecretManifest struct {
	Required []string `json:"required"`
	Optional []string `json:"optional"`
}

// Instance is a registered gate instance.
type Instance struct {
	InstanceID     string         `json:"instance_id"`
	KindID         string         `json:"kind_id"`
	Label          string         `json:"label"`
	SubjectID      int64          `json:"subject_id"`
	ActiveRevision uint64         `json:"active_revision"`
	Present        bool           `json:"present"`
	Enabled        bool           `json:"enabled"`
	ConfigB64      string         `json:"config_b64"`
	ConfigDigest   string         `json:"config_digest"`
	CreatedAt      int64          `json:"created_at"`
	Connection     Connection     `json:"connection"`
	SecretManifest SecretManifest `json:"secret_manifest"`
}

// CatchUpStart is the binding PUT request's catch-up start. Mode is
// "now" | "beginning" | "supplied"; CursorB64 is present exactly when
// Mode is "supplied" (an extra member would be an unknown field).
type CatchUpStart struct {
	Mode      string  `json:"mode"`
	CursorB64 *string `json:"cursor_b64,omitempty"`
}

// StoredStart is the response form: mode only, never cursor bytes.
type StoredStart struct {
	Mode string `json:"mode"`
}

// AdminBinding is a registered gate binding (the wire-side bind target
// keeps the short Binding name from G1).
type AdminBinding struct {
	BindingID          string       `json:"binding_id"`
	InstanceID         string       `json:"instance_id"`
	Address            string       `json:"address"`
	Label              *string      `json:"label"`
	BindingMetadataB64 string       `json:"binding_metadata_b64"`
	Purposes           []string     `json:"purposes"`
	CatchUpStart       *StoredStart `json:"catch_up_start"`
	PlacePublicKey     string       `json:"place_public_key"`
	SubjectID          int64        `json:"subject_id"`
	ClosedAt           *int64       `json:"closed_at"`
	CloseReason        *string      `json:"close_reason"`
	CursorDigest       *string      `json:"cursor_digest"`
}

// ---- request payloads (spec §3.2, exact member sets) ----------------

// SchemaPut is the PUT /api/gate-schemas/{schema_id} request.
type SchemaPut struct {
	Role        string `json:"role"`
	Format      string `json:"format"`
	DocumentB64 string `json:"document_b64"`
}

// KindPut is the PUT /api/gate-kinds/{kind_id} request. Registration
// is not a request member — the server records external.
type KindPut struct {
	ProtocolMajor           uint32  `json:"protocol_major"`
	OriginScope             string  `json:"origin_scope"`
	IngressDiscovery        string  `json:"ingress_discovery"`
	AddressForm             *string `json:"address_form"`
	ConfigSchemaID          *string `json:"config_schema_id"`
	BindingMetadataSchemaID *string `json:"binding_metadata_schema_id"`
	SecretManifestSchemaID  *string `json:"secret_manifest_schema_id"`
	CatchUpMode             *string `json:"catch_up_mode"`
	CursorSchemaID          *string `json:"cursor_schema_id"`
}

// InstancePut is the PUT /api/gate-instances/{instance_id} request.
type InstancePut struct {
	KindID    string `json:"kind_id"`
	Label     string `json:"label"`
	SubjectID int64  `json:"subject_id"`
	Enabled   bool   `json:"enabled"`
	ConfigB64 string `json:"config_b64"`
}

// RevisionPost is the POST /api/gate-instances/{id}/revisions request.
type RevisionPost struct {
	ExpectedActiveRevision uint64 `json:"expected_active_revision"`
	Enabled                bool   `json:"enabled"`
	ConfigB64              string `json:"config_b64"`
}

// BindingPut is the PUT /api/gate-bindings/{binding_id} request.
// Purposes is not a request member — the server always creates the
// literal ["inbound","outbound"] pair.
type BindingPut struct {
	InstanceID         string        `json:"instance_id"`
	Address            string        `json:"address"`
	Label              *string       `json:"label"`
	BindingMetadataB64 string        `json:"binding_metadata_b64"`
	CatchUpStart       *CatchUpStart `json:"catch_up_start"`
}

// ---- non-DTO success bodies -----------------------------------------

// InstanceDeleted is the DELETE instance response. Deleted=false means
// the instance was already tombstoned (write-zero).
type InstanceDeleted struct {
	InstanceID string `json:"instance_id"`
	Deleted    bool   `json:"deleted"`
	Revision   uint64 `json:"revision"`
}

// RevisionCreated is the POST revisions response.
type RevisionCreated struct {
	InstanceID   string `json:"instance_id"`
	Revision     uint64 `json:"revision"`
	ConfigDigest string `json:"config_digest"`
	Enabled      bool   `json:"enabled"`
}

// BindingClosed is the DELETE binding response.
type BindingClosed struct {
	BindingID string `json:"binding_id"`
	Closed    bool   `json:"closed"`
}

// ---- client ---------------------------------------------------------

// AdminClient talks to the gate admin registration API with operator
// credentials. The token is sent as an Authorization bearer header —
// "既存operator認証" per spec §3; the exact header shape is the
// operator-auth convention of the deployment.
type AdminClient struct {
	baseURL string
	token   string
	hc      *http.Client
}

// NewAdminClient builds a client for the admin plane at baseURL
// (scheme://host, no trailing slash needed) using operatorToken.
func NewAdminClient(baseURL, operatorToken string) *AdminClient {
	return &AdminClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   operatorToken,
		hc:      &http.Client{Timeout: 30 * time.Second},
	}
}

// clientErr is a request rejected client-side before any wire I/O
// (grammar violations the server would answer 400/422 to anyway).
func clientErr(format string, a ...any) error {
	return fmt.Errorf("gate admin: "+format, a...)
}

// PutSchema creates (201) or matches byte-equivalent (200) a schema.
// created reports which.
func (c *AdminClient) PutSchema(ctx context.Context, schemaID string, req SchemaPut) (out *Schema, created bool, err error) {
	if schemaID == "" {
		return nil, false, clientErr("schema id must be nonempty")
	}
	if !isStdPaddedBase64(req.DocumentB64) {
		return nil, false, clientErr("document_b64 must be standard padded base64")
	}
	out = &Schema{}
	created, err = c.do(ctx, http.MethodPut, "/api/gate-schemas/"+schemaID, req, out)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// GetSchema fetches one schema.
func (c *AdminClient) GetSchema(ctx context.Context, schemaID string) (*Schema, error) {
	if schemaID == "" {
		return nil, clientErr("schema id must be nonempty")
	}
	out := &Schema{}
	if _, err := c.do(ctx, http.MethodGet, "/api/gate-schemas/"+schemaID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutKind creates (201) or equivalently replaces (200) a kind.
func (c *AdminClient) PutKind(ctx context.Context, kindID string, req KindPut) (out *Kind, created bool, err error) {
	if kindID == "" {
		return nil, false, clientErr("kind id must be nonempty")
	}
	out = &Kind{}
	created, err = c.do(ctx, http.MethodPut, "/api/gate-kinds/"+kindID, req, out)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// GetKind fetches one kind.
func (c *AdminClient) GetKind(ctx context.Context, kindID string) (*Kind, error) {
	if kindID == "" {
		return nil, clientErr("kind id must be nonempty")
	}
	out := &Kind{}
	if _, err := c.do(ctx, http.MethodGet, "/api/gate-kinds/"+kindID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutInstance creates (201) or matches aggregate-equivalent (200) an
// instance. The path id must be a UUIDv7 (spec §3.2).
func (c *AdminClient) PutInstance(ctx context.Context, instanceID string, req InstancePut) (out *Instance, created bool, err error) {
	if !isUUIDv7(instanceID) {
		return nil, false, clientErr("instance id must be a canonical lowercase UUIDv7")
	}
	if !isStdPaddedBase64(req.ConfigB64) {
		return nil, false, clientErr("config_b64 must be standard padded base64")
	}
	out = &Instance{}
	created, err = c.do(ctx, http.MethodPut, "/api/gate-instances/"+instanceID, req, out)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// GetInstance fetches one instance.
func (c *AdminClient) GetInstance(ctx context.Context, instanceID string) (*Instance, error) {
	if !isCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	out := &Instance{}
	if _, err := c.do(ctx, http.MethodGet, "/api/gate-instances/"+instanceID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteInstance tombstones an instance (closing its bindings/places).
// Already-tombstoned answers Deleted=false, not an error.
func (c *AdminClient) DeleteInstance(ctx context.Context, instanceID string) (*InstanceDeleted, error) {
	if !isCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	out := &InstanceDeleted{}
	if _, err := c.do(ctx, http.MethodDelete, "/api/gate-instances/"+instanceID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PostRevision appends a new config revision (CAS on the active one).
func (c *AdminClient) PostRevision(ctx context.Context, instanceID string, req RevisionPost) (*RevisionCreated, error) {
	if !isCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	if !isStdPaddedBase64(req.ConfigB64) {
		return nil, clientErr("config_b64 must be standard padded base64")
	}
	out := &RevisionCreated{}
	if _, err := c.do(ctx, http.MethodPost, "/api/gate-instances/"+instanceID+"/revisions", req, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutBinding creates (201) or matches byte-equivalent (200) a binding.
// The path id must be a UUIDv7 (spec §3.2).
func (c *AdminClient) PutBinding(ctx context.Context, bindingID string, req BindingPut) (out *AdminBinding, created bool, err error) {
	if !isUUIDv7(bindingID) {
		return nil, false, clientErr("binding id must be a canonical lowercase UUIDv7")
	}
	if !isCanonicalUUID(req.InstanceID) {
		return nil, false, clientErr("instance_id must be a canonical lowercase UUID")
	}
	if !isStdPaddedBase64(req.BindingMetadataB64) {
		return nil, false, clientErr("binding_metadata_b64 must be standard padded base64")
	}
	if s := req.CatchUpStart; s != nil && s.Mode == "supplied" &&
		(s.CursorB64 == nil || !isStdPaddedBase64(*s.CursorB64)) {
		return nil, false, clientErr("catch_up_start cursor_b64 must be standard padded base64")
	}
	out = &AdminBinding{}
	created, err = c.do(ctx, http.MethodPut, "/api/gate-bindings/"+bindingID, req, out)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// GetBinding fetches one binding.
func (c *AdminClient) GetBinding(ctx context.Context, bindingID string) (*AdminBinding, error) {
	if !isCanonicalUUID(bindingID) {
		return nil, clientErr("binding id must be a canonical lowercase UUID")
	}
	out := &AdminBinding{}
	if _, err := c.do(ctx, http.MethodGet, "/api/gate-bindings/"+bindingID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteBinding closes a binding.
func (c *AdminClient) DeleteBinding(ctx context.Context, bindingID string) (*BindingClosed, error) {
	if !isCanonicalUUID(bindingID) {
		return nil, clientErr("binding id must be a canonical lowercase UUID")
	}
	out := &BindingClosed{}
	if _, err := c.do(ctx, http.MethodDelete, "/api/gate-bindings/"+bindingID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// do issues one admin request. body==nil sends no body (GET/DELETE).
// Success (2xx) decodes into out and reports created=(201). Any other
// status must carry the uniform error envelope and becomes *AdminError.
func (c *AdminClient) do(ctx context.Context, method, path string, body, out any) (created bool, err error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return false, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return false, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false, fmt.Errorf("gate admin: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, fmt.Errorf("gate admin: %s %s: read response: %w", method, path, err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			if err := json.Unmarshal(raw, out); err != nil {
				return false, fmt.Errorf("gate admin: %s %s: bad success body: %w", method, path, err)
			}
		}
		return resp.StatusCode == http.StatusCreated, nil
	}

	// Every non-2xx must be the uniform envelope, and the (status,
	// code) pair must sit inside the spec's total map — anything else
	// is a contract violation worth failing loudly on.
	var env struct {
		Error *struct {
			Code   string  `json:"code"`
			At     *string `json:"at"`
			Detail *string `json:"detail"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) != nil || env.Error == nil || env.Error.Code == "" {
		return false, fmt.Errorf("gate admin: %s %s: HTTP %d with non-envelope body %q",
			method, path, resp.StatusCode, truncate(raw, 200))
	}
	if want, known := adminStatusCodes[env.Error.Code]; !known || want != resp.StatusCode {
		return false, fmt.Errorf("gate admin: %s %s: HTTP %d carries out-of-contract code %q",
			method, path, resp.StatusCode, env.Error.Code)
	}
	return false, &AdminError{
		Status: resp.StatusCode,
		Code:   env.Error.Code,
		At:     env.Error.At,
		Detail: env.Error.Detail,
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
