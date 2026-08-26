package gate

// Admin-plane client for the external gate V3 provisioning API
// (DESIGN-EXTGATE-V3.md §5). The surface is exactly 6 operations:
// Instance GET/PUT/DELETE, revision POST, Binding PUT/DELETE. Schema,
// kind, Binding GET, and cursor APIs no longer exist. The error
// envelope is {"error":{code,detail}} with the §5.4 stable code
// vocabulary; field order is non-semantic and unknown members are
// ignored server-side (duplicates are rejected).

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
// {"error":{code,detail}} plus the HTTP status it arrived with. Detail
// mirrors the wire: always present, possibly null.
type AdminError struct {
	Status int
	Code   string
	Detail *string
}

func (e *AdminError) Error() string {
	msg := fmt.Sprintf("gate admin: %s (HTTP %d)", e.Code, e.Status)
	if e.Detail != nil {
		msg += ": " + *e.Detail
	}
	return msg
}

// adminStatusCodes is the §5.4 map of every HTTP-carried stable error
// code and the one status it may arrive with. Used to fail loudly on an
// envelope that contradicts the contract.
var adminStatusCodes = map[string]int{
	"unauthorized":       401,
	"bad_request":        400,
	"subject_unknown":    404,
	"instance_unknown":   404,
	"binding_unknown":    404,
	"instance_conflict":  409,
	"revision_conflict":  409,
	"instance_disabled":  409,
	"binding_closed":     409,
	"binding_conflict":   409,
	"address_in_use":     409,
	"instance_active":    409,
	"instance_not_ready": 409,
	"store_error":        500,
}

// ---- DTOs (spec §5.2) -----------------------------------------------

// Instance is a registered gate instance row.
type Instance struct {
	InstanceID   string `json:"instance_id"`
	KindID       string `json:"kind_id"`
	SubjectID    int64  `json:"subject_id"`
	Revision     uint64 `json:"revision"`
	Enabled      bool   `json:"enabled"`
	ConfigB64    string `json:"config_b64"`
	ConfigDigest string `json:"config_digest"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	DeletedAt    *int64 `json:"deleted_at"`
}

// AdminBinding is a registered gate binding row (the wire-side bind
// target keeps the short Binding name).
type AdminBinding struct {
	BindingID  string `json:"binding_id"`
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
	CreatedAt  int64  `json:"created_at"`
	ClosedAt   *int64 `json:"closed_at"`
}

// ---- request payloads (spec §5.3, exact member sets) ----------------

// InstancePut is the PUT /api/gate-instances/{instance_id} request.
type InstancePut struct {
	KindID    string `json:"kind_id"`
	SubjectID int64  `json:"subject_id"`
	Enabled   bool   `json:"enabled"`
	ConfigB64 string `json:"config_b64"`
}

// RevisionPost is the POST /api/gate-instances/{id}/revisions request.
type RevisionPost struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	Enabled          bool   `json:"enabled"`
	ConfigB64        string `json:"config_b64"`
}

// BindingPut is the PUT /api/gate-bindings/{binding_id} request.
type BindingPut struct {
	InstanceID string `json:"instance_id"`
	Address    string `json:"address"`
}

// ---- non-DTO success bodies -----------------------------------------

// InstanceDeleted is the DELETE instance response.
type InstanceDeleted struct {
	InstanceID string `json:"instance_id"`
	Deleted    bool   `json:"deleted"`
}

// RevisionCreated is the POST revisions response.
type RevisionCreated struct {
	InstanceID   string `json:"instance_id"`
	Revision     uint64 `json:"revision"`
	Enabled      bool   `json:"enabled"`
	ConfigDigest string `json:"config_digest"`
}

// BindingClosed is the DELETE binding response. Closed is true even
// when the binding was already closed (write-zero response, §5.3).
type BindingClosed struct {
	BindingID string `json:"binding_id"`
	Closed    bool   `json:"closed"`
}

// ---- client ---------------------------------------------------------

// AdminClient talks to the gate admin plane with the operator Bearer
// token (§5.1).
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
// (grammar violations the server would answer 400 to anyway).
func clientErr(format string, a ...any) error {
	return fmt.Errorf("gate admin: "+format, a...)
}

// GetInstance fetches one instance (deleted rows answer
// instance_unknown).
func (c *AdminClient) GetInstance(ctx context.Context, instanceID string) (*Instance, error) {
	if !IsCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	out := &Instance{}
	if _, err := c.do(ctx, http.MethodGet, "/api/gate-instances/"+instanceID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PutInstance creates (201) or matches byte-equivalent existing (200)
// an instance at revision 1. created reports which.
func (c *AdminClient) PutInstance(ctx context.Context, instanceID string, req InstancePut) (out *Instance, created bool, err error) {
	if !IsCanonicalUUID(instanceID) {
		return nil, false, clientErr("instance id must be a canonical lowercase UUID")
	}
	if req.KindID == "" {
		return nil, false, clientErr("kind_id must be nonempty")
	}
	if req.SubjectID <= 0 {
		return nil, false, clientErr("subject_id must be positive")
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

// DeleteInstance deletes an instance (closing its open bindings in the
// same transaction). A live instance answers 409 instance_active.
func (c *AdminClient) DeleteInstance(ctx context.Context, instanceID string) (*InstanceDeleted, error) {
	if !IsCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	out := &InstanceDeleted{}
	if _, err := c.do(ctx, http.MethodDelete, "/api/gate-instances/"+instanceID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// PostRevision bumps the active revision by exactly one (CAS on
// expected_revision). A live instance answers 409 instance_active.
func (c *AdminClient) PostRevision(ctx context.Context, instanceID string, req RevisionPost) (*RevisionCreated, error) {
	if !IsCanonicalUUID(instanceID) {
		return nil, clientErr("instance id must be a canonical lowercase UUID")
	}
	if req.ExpectedRevision == 0 {
		return nil, clientErr("expected_revision must be positive")
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

// PutBinding creates (201) or matches byte-equivalent open row (200) a
// binding. Legal while the instance is live (§5.5).
func (c *AdminClient) PutBinding(ctx context.Context, bindingID string, req BindingPut) (out *AdminBinding, created bool, err error) {
	if !IsCanonicalUUID(bindingID) {
		return nil, false, clientErr("binding id must be a canonical lowercase UUID")
	}
	if !IsCanonicalUUID(req.InstanceID) {
		return nil, false, clientErr("instance_id must be a canonical lowercase UUID")
	}
	if req.Address == "" {
		return nil, false, clientErr("address must be nonempty")
	}
	out = &AdminBinding{}
	created, err = c.do(ctx, http.MethodPut, "/api/gate-bindings/"+bindingID, req, out)
	if err != nil {
		return nil, false, err
	}
	return out, created, nil
}

// DeleteBinding closes a binding. Already-closed answers the same
// write-zero response. Legal while the instance is live (§5.5).
func (c *AdminClient) DeleteBinding(ctx context.Context, bindingID string) (*BindingClosed, error) {
	if !IsCanonicalUUID(bindingID) {
		return nil, clientErr("binding id must be a canonical lowercase UUID")
	}
	out := &BindingClosed{}
	if _, err := c.do(ctx, http.MethodDelete, "/api/gate-bindings/"+bindingID, nil, out); err != nil {
		return nil, err
	}
	return out, nil
}

// do issues one admin request. body==nil sends no body (GET/DELETE have
// 0-byte request bodies, §5.3). Success (2xx) decodes into out and
// reports created=(201). Any other status must carry the uniform error
// envelope and becomes *AdminError.
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
	// code) pair must sit inside the §5.4 map — anything else is a
	// contract violation worth failing loudly on.
	var env struct {
		Error *struct {
			Code   string  `json:"code"`
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
		Detail: env.Error.Detail,
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "…"
}
