package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/zenryokukikai/omoikane/internal/auth"
	"github.com/zenryokukikai/omoikane/internal/store"
)

// ----------------------------------------------------------------------
// /v1/attachments — file/image evidence (see spec X-YCXLOW)
//
//   POST /v1/attachments         multipart/form-data upload
//   GET  /v1/attachments/{id}    metadata JSON
//   GET  /v1/attachments/{id}/content   raw bytes streamed
//
// Auth: write scope for upload, read for fetch. project_id is on the
// row but project-level ACL isn't enforced yet — slice 1 treats
// "authenticated read" as sufficient. Tighter ACL is v2.
// ----------------------------------------------------------------------

// postAttachment handles multipart upload. Form fields:
//   project_id (text)   required, must exist
//   role       (text)   required, must be in standard vocab
//   caption    (text)   required, non-blank after trim
//   file       (file)   required, non-empty, ≤ AttachmentMaxBytes
//
// The Content-Type of the file part is propagated as the attachment's
// mime. If absent, defaults to application/octet-stream.
func (h *Handler) postAttachment(w http.ResponseWriter, r *http.Request) {
	tok := auth.FromContext(r.Context())
	if tok == nil || tok.UserID == "" {
		writeError(w, http.StatusUnauthorized, CodeInvalidToken, "auth required", nil)
		return
	}

	// 32 MiB in-memory threshold; larger files spool to a temp file
	// inside r.MultipartReader. http.MaxBytesReader (installed at the
	// route group) already enforces the upper bound on the whole body.
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		// MaxBytesReader returns a *http.MaxBytesError when exceeded.
		var mbErr *http.MaxBytesError
		if errors.As(err, &mbErr) {
			writeError(w, http.StatusRequestEntityTooLarge, CodeBodyTooLarge,
				fmt.Sprintf("upload exceeds %d bytes", mbErr.Limit), nil)
			return
		}
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"could not parse multipart form: "+err.Error(), nil)
		return
	}

	projectID := r.FormValue("project_id")
	role := r.FormValue("role")
	caption := r.FormValue("caption")
	if projectID == "" || role == "" || caption == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"project_id, role, caption are all required form fields", nil)
		return
	}
	if !store.ValidAttachmentRole(role) {
		writeError(w, http.StatusBadRequest, CodeBadRequest,
			"unknown role; must be one of the standard vocabulary",
			map[string]any{"got": role, "valid": store.AttachmentRoleVocab()})
		return
	}

	// Confirm the project exists — otherwise the attachment row's FK
	// would fail with a less-friendly error, and worse, the upload
	// would already be on disk by then.
	if _, err := h.Store.GetProject(httpCtx(r), projectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest, CodeBadRequest,
				"project does not exist", map[string]any{"project_id": projectID})
			return
		}
		writeStoreError(w, err)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeMissingFields,
			"file field required (multipart/form-data)", nil)
		return
	}
	defer file.Close()

	mime := header.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}

	maxBytes := h.AttachmentMaxBytes
	if maxBytes <= 0 {
		maxBytes = 50 << 20 // sane default in case nothing was wired up
	}
	att, err := h.Store.CreateAttachment(httpCtx(r), store.CreateAttachmentParams{
		ProjectID:  projectID,
		Mime:       mime,
		Filename:   header.Filename,
		Role:       role,
		Caption:    caption,
		UploadedBy: tok.UserID,
		Content:    file,
		MaxBytes:   maxBytes,
	})
	if err != nil {
		// Most failures here are user input issues; surface as 400.
		if errors.Is(err, store.ErrInvalidInput) {
			writeError(w, http.StatusBadRequest, CodeBadRequest, err.Error(), nil)
			return
		}
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, att)
}

// getAttachment returns the metadata row by id. No content.
func (h *Handler) getAttachment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "id required", nil)
		return
	}
	a, err := h.Store.GetAttachment(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

// getAttachmentContent streams the raw blob. Content-Type and
// Content-Length are set from the stored mime + size_bytes so vision-
// capable agents (and browsers) treat the response correctly.
//
// The streaming model means we don't load the file into memory even
// for large attachments — useful for video / model artifacts down the
// road.
func (h *Handler) getAttachmentContent(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if id == "" {
		writeError(w, http.StatusBadRequest, CodeMissingFields, "id required", nil)
		return
	}
	rc, a, err := h.Store.OpenAttachmentContent(httpCtx(r), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", a.Mime)
	w.Header().Set("Content-Length", strconv.FormatInt(a.SizeBytes, 10))
	if a.Filename != "" {
		// inline disposition: the browser tries to render (image/<video>
		// tag, PDF viewer, etc.) rather than force-downloading. Agents
		// don't care about the header; this is for the dashboard.
		w.Header().Set("Content-Disposition",
			"inline; filename="+strconv.Quote(a.Filename))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}
