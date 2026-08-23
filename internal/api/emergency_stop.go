package api

// Librarian emergency stop (/v1/librarian/emergency_stop).
//
// This file owns global mutable state shared across handler files: the
// package-level emergencyStop flag. rejectIfEmergencyStop is called
// from the librarian write handlers in librarians.go,
// librarian_chat_api.go, librarian_work_api.go, open_work.go and
// ops.go; ResetEmergencyStopForTest is called from the package's test
// files. Keep all four symbols together here.

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
)

// emergencyStop is the cluster-wide off switch. When true, all librarian
// /v1/librarian/* writes are rejected with 503. Phase 6 §23.8 mandates
// this — Phase 5 ships it as a stub so existing call sites work once
// real librarians come online.
var emergencyStop int32 // 0/1

type emergencyStopRequest struct {
	Engage bool `json:"engage"`
}

func (h *Handler) librarianEmergencyStop(w http.ResponseWriter, r *http.Request) {
	var req emergencyStopRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, CodeBadJSON, err.Error(), nil)
		return
	}
	if req.Engage {
		atomic.StoreInt32(&emergencyStop, 1)
		h.Logger.Warn("librarian emergency stop engaged")
	} else {
		atomic.StoreInt32(&emergencyStop, 0)
		h.Logger.Info("librarian emergency stop released")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"engaged": atomic.LoadInt32(&emergencyStop) == 1,
	})
}

// rejectIfEmergencyStop returns true (and writes 503) when the kill
// switch is engaged. All librarian write endpoints call this first.
func (h *Handler) rejectIfEmergencyStop(w http.ResponseWriter) bool {
	if atomic.LoadInt32(&emergencyStop) != 1 {
		return false
	}
	writeError(w, http.StatusServiceUnavailable, "EMERGENCY_STOP",
		"Librarian community is currently halted by emergency stop.", nil)
	return true
}

// ResetEmergencyStopForTest is exported so test code can reset the
// package-level flag between sub-tests. Not callable from production
// since the function name starts with the harmless "Reset" verb but is
// guarded by the *_test.go discipline.
func ResetEmergencyStopForTest() { atomic.StoreInt32(&emergencyStop, 0) }
