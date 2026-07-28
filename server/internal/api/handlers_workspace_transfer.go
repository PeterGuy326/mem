package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"syscall"
	"time"

	"github.com/PeterGuy326/mem/server/internal/workspacebundle"
	"github.com/PeterGuy326/mem/server/internal/workspacetransfer"
)

const defaultWorkspaceBundleMaxBytes int64 = 8 << 30

type workspaceObjectCountsResponse struct {
	Folders            int64 `json:"folders"`
	Files              int64 `json:"files"`
	Memories           int64 `json:"memories"`
	MemoryEvents       int64 `json:"memory_events"`
	Tasks              int64 `json:"tasks"`
	Checkpoints        int64 `json:"checkpoints"`
	CheckpointRefs     int64 `json:"checkpoint_refs"`
	CheckpointPayloads int64 `json:"checkpoint_payloads"`
	Blobs              int64 `json:"blobs"`
	BlobBytes          int64 `json:"blob_bytes"`
}

type workspaceImportResponse struct {
	BundleID          string                        `json:"bundle_id"`
	ArchiveSHA256     string                        `json:"archive_sha256"`
	SourceWorkspaceID string                        `json:"source_workspace_id"`
	ImportedAt        time.Time                     `json:"imported_at"`
	Counts            workspaceObjectCountsResponse `json:"counts"`
	Replayed          bool                          `json:"replayed"`
}

type workspaceImportConflictResponse struct {
	Error     string                            `json:"error"`
	Hint      string                            `json:"hint"`
	Conflicts []workspaceImportConflictResource `json:"conflicts"`
	Total     int                               `json:"total,omitempty"`
	Truncated bool                              `json:"truncated,omitempty"`
}

type workspaceImportConflictResource struct {
	Kind     string `json:"kind"`
	Resource string `json:"resource,omitempty"`
	Value    string `json:"value,omitempty"`
}

func (s *Server) handleWorkspaceExport(w http.ResponseWriter, r *http.Request) {
	if s.WorkspaceTransfer == nil {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"workspace_transfer_unavailable",
			"workspace transfer is not configured",
		)
		return
	}

	archive, err := os.CreateTemp(
		s.WorkspaceTransferTmpDir,
		"mem-workspace-export-*.membundle",
	)
	if err != nil {
		s.logWorkspaceTransferError("create export temporary file", err)
		if isWorkspaceTransferStorageExhausted(err) {
			writeWorkspaceTransferStorageExhausted(w)
			return
		}
		writeWorkspaceTransferInternalError(w)
		return
	}
	archivePath := archive.Name()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archivePath)
	}()
	if err := archive.Chmod(0o600); err != nil {
		s.logWorkspaceTransferError("secure export temporary file", err)
		writeWorkspaceTransferInternalError(w)
		return
	}

	ws := currentWorkspace(r)
	if _, err := s.WorkspaceTransfer.Export(
		r.Context(),
		workspacetransfer.ExportRequest{
			WorkspaceID: ws.ID,
			Writer:      archive,
		},
	); err != nil {
		s.writeWorkspaceTransferServiceError(w, "export", err)
		return
	}
	if err := archive.Sync(); err != nil {
		s.logWorkspaceTransferError("sync export temporary file", err)
		if isWorkspaceTransferStorageExhausted(err) {
			writeWorkspaceTransferStorageExhausted(w)
			return
		}
		writeWorkspaceTransferInternalError(w)
		return
	}
	info, err := archive.Stat()
	if err != nil {
		s.logWorkspaceTransferError("stat export temporary file", err)
		writeWorkspaceTransferInternalError(w)
		return
	}
	if info.Size() > s.workspaceBundleMaxBytes() {
		writeWorkspaceBundleTooLarge(w)
		return
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		s.logWorkspaceTransferError("rewind export temporary file", err)
		writeWorkspaceTransferInternalError(w)
		return
	}

	filename := fmt.Sprintf("workspace-%s.membundle", ws.ID)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
	w.Header().Set("Content-Type", workspacebundle.BundleMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, archive); err != nil {
		s.logWorkspaceTransferError("send workspace export", err)
	}
}

func (s *Server) handleWorkspaceImport(w http.ResponseWriter, r *http.Request) {
	if s.WorkspaceTransfer == nil {
		writeError(
			w,
			http.StatusServiceUnavailable,
			"workspace_transfer_unavailable",
			"workspace transfer is not configured",
		)
		return
	}
	if r.Header.Get("Content-Type") != workspacebundle.BundleMediaType {
		writeError(
			w,
			http.StatusUnsupportedMediaType,
			"unsupported_media_type",
			"Content-Type must be "+workspacebundle.BundleMediaType,
		)
		return
	}
	modes, present := r.URL.Query()["mode"]
	if !present || len(modes) != 1 || modes[0] != workspacetransfer.RestoreModeFresh {
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"unsupported_restore_mode",
			"mode must be "+workspacetransfer.RestoreModeFresh,
		)
		return
	}
	maxBytes := s.workspaceBundleMaxBytes()
	if r.ContentLength > maxBytes {
		writeWorkspaceBundleTooLarge(w)
		return
	}

	archive, err := os.CreateTemp(
		s.WorkspaceTransferTmpDir,
		"mem-workspace-import-*.membundle",
	)
	if err != nil {
		s.logWorkspaceTransferError("create import temporary file", err)
		if isWorkspaceTransferStorageExhausted(err) {
			writeWorkspaceTransferStorageExhausted(w)
			return
		}
		writeWorkspaceTransferInternalError(w)
		return
	}
	archivePath := archive.Name()
	defer func() {
		_ = archive.Close()
		_ = os.Remove(archivePath)
	}()
	if err := archive.Chmod(0o600); err != nil {
		s.logWorkspaceTransferError("secure import temporary file", err)
		writeWorkspaceTransferInternalError(w)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	size, err := io.Copy(archive, r.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeWorkspaceBundleTooLarge(w)
			return
		}
		s.logWorkspaceTransferError("read workspace import request", err)
		if isWorkspaceTransferStorageExhausted(err) {
			writeWorkspaceTransferStorageExhausted(w)
			return
		}
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_workspace_bundle",
			"workspace bundle request body could not be read",
		)
		return
	}
	if size == 0 {
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_workspace_bundle",
			"workspace bundle request body is empty",
		)
		return
	}
	if err := archive.Sync(); err != nil {
		s.logWorkspaceTransferError("sync import temporary file", err)
		if isWorkspaceTransferStorageExhausted(err) {
			writeWorkspaceTransferStorageExhausted(w)
			return
		}
		writeWorkspaceTransferInternalError(w)
		return
	}

	result, err := s.WorkspaceTransfer.Import(
		r.Context(),
		workspacetransfer.ImportRequest{
			WorkspaceID: currentWorkspace(r).ID,
			Mode:        modes[0],
			Reader:      archive,
			Size:        size,
		},
	)
	if err != nil {
		s.writeWorkspaceTransferServiceError(w, "import", err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceImportResponse{
		BundleID:          result.BundleID.String(),
		ArchiveSHA256:     result.ArchiveSHA256,
		SourceWorkspaceID: result.SourceWorkspaceID.String(),
		ImportedAt:        result.ImportedAt,
		Counts:            workspaceCountsResponse(result.Counts),
		Replayed:          result.Replayed,
	})
}

func workspaceCountsResponse(counts workspacebundle.ObjectCounts) workspaceObjectCountsResponse {
	return workspaceObjectCountsResponse{
		Folders:            counts.Folders,
		Files:              counts.Files,
		Memories:           counts.Memories,
		MemoryEvents:       counts.MemoryEvents,
		Tasks:              counts.Tasks,
		Checkpoints:        counts.Checkpoints,
		CheckpointRefs:     counts.CheckpointRefs,
		CheckpointPayloads: counts.CheckpointPayloads,
		Blobs:              counts.Blobs,
		BlobBytes:          counts.BlobBytes,
	}
}

func (s *Server) writeWorkspaceTransferServiceError(
	w http.ResponseWriter,
	operation string,
	err error,
) {
	switch {
	case isWorkspaceTransferStorageExhausted(err):
		s.logWorkspaceTransferError(operation+" workspace storage exhausted", err)
		writeWorkspaceTransferStorageExhausted(w)
	case errors.Is(err, workspacetransfer.ErrNotConfigured):
		writeError(
			w,
			http.StatusServiceUnavailable,
			"workspace_transfer_unavailable",
			"workspace transfer is not configured",
		)
	case errors.Is(err, workspacetransfer.ErrWorkspaceNotFound):
		writeError(
			w,
			http.StatusNotFound,
			"workspace_not_found",
			"workspace no longer exists",
		)
	case errors.Is(err, workspacebundle.ErrLimitExceeded):
		writeWorkspaceBundleTooLarge(w)
	case errors.Is(err, workspacetransfer.ErrConflict):
		var conflictError *workspacetransfer.ConflictError
		conflicts := make([]workspaceImportConflictResource, 0)
		if errors.As(err, &conflictError) {
			conflicts = make(
				[]workspaceImportConflictResource,
				0,
				len(conflictError.Conflicts),
			)
			for _, conflict := range conflictError.Conflicts {
				conflicts = append(conflicts, workspaceImportConflictResource{
					Kind:     conflict.Kind,
					Resource: conflict.Resource,
					Value:    conflict.Value,
				})
			}
		}
		total := len(conflicts)
		truncated := false
		if conflictError != nil {
			if conflictError.Total > total {
				total = conflictError.Total
			}
			truncated = conflictError.Truncated
		}
		writeJSON(w, http.StatusConflict, workspaceImportConflictResponse{
			Error:     "workspace_import_conflict",
			Hint:      "target workspace conflicts with this fresh import",
			Conflicts: conflicts,
			Total:     total,
			Truncated: truncated,
		})
	case errors.Is(err, workspacetransfer.ErrUnsupportedMode),
		errors.Is(err, workspacebundle.ErrUnsupportedVersion):
		writeError(
			w,
			http.StatusUnprocessableEntity,
			"unsupported_workspace_bundle",
			"workspace bundle version or restore mode is not supported",
		)
	case errors.Is(err, workspacetransfer.ErrIntegrity),
		errors.Is(err, workspacebundle.ErrInvalidBundle),
		errors.Is(err, workspacebundle.ErrUnsafeArchive),
		errors.Is(err, workspacebundle.ErrIntegrity),
		errors.Is(err, workspacebundle.ErrDependency):
		writeError(
			w,
			http.StatusBadRequest,
			"invalid_workspace_bundle",
			"workspace bundle failed validation",
		)
	default:
		s.logWorkspaceTransferError(operation+" workspace", err)
		writeWorkspaceTransferInternalError(w)
	}
}

func (s *Server) workspaceBundleMaxBytes() int64 {
	if s.WorkspaceBundleMaxBytes > 0 {
		return s.WorkspaceBundleMaxBytes
	}
	return defaultWorkspaceBundleMaxBytes
}

func isWorkspaceTransferStorageExhausted(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

func (s *Server) logWorkspaceTransferError(message string, err error) {
	if s.Log != nil {
		s.Log.Error(message, "err", err)
	}
}

func writeWorkspaceBundleTooLarge(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusRequestEntityTooLarge,
		"workspace_bundle_too_large",
		"workspace bundle exceeds the configured transfer limit",
	)
}

func writeWorkspaceTransferInternalError(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusInternalServerError,
		"workspace_transfer_failed",
		"workspace transfer failed; check server logs",
	)
}

func writeWorkspaceTransferStorageExhausted(w http.ResponseWriter) {
	writeError(
		w,
		http.StatusInsufficientStorage,
		"workspace_transfer_storage_exhausted",
		"workspace transfer temporary storage is exhausted",
	)
}
