package transport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/ersinkoc/dfmt/internal/sandbox"
)

type EditParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// EditResponse is the response from an edit operation.
type EditResponse struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// WriteParams are the parameters for the Write method.
type WriteParams struct {
	ProjectID string `json:"project_id,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// WriteResponse is the response from a write operation.
type WriteResponse struct {
	Success bool   `json:"success"`
	Summary string `json:"summary"`
}

// Edit performs an edit on a file via the sandbox.
func (h *Handlers) Edit(ctx context.Context, params EditParams) (_ *EditResponse, err error) {
	defer recordToolCall("edit", ctx, &err, time.Now())
	h.touch()
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
	release, err := acquireLimiter(ctx, h.writeSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.EditReq{
		Path:      params.Path,
		OldString: params.OldString,
		NewString: params.NewString,
	}

	resp, err := bundle.Sandbox.Edit(ctx, req)
	if err != nil {
		return nil, err
	}

	h.logEvent(ctx, "tool.edit", params.Path, map[string]any{
		"path": params.Path,
	})

	return &EditResponse{
		Success: resp.Success,
		Summary: resp.Summary,
	}, nil
}

// Write writes content to a file via the sandbox.
func (h *Handlers) Write(ctx context.Context, params WriteParams) (_ *WriteResponse, err error) {
	defer recordToolCall("write", ctx, &err, time.Now())
	h.touch()
	bundle, berr := h.resolveBundle(ctx)
	if berr != nil {
		return nil, berr
	}
	if bundle.Sandbox == nil {
		return nil, errNoProject
	}
	release, err := acquireLimiter(ctx, h.writeSem)
	if err != nil {
		return nil, err
	}
	defer release()
	req := sandbox.WriteReq{
		Path:    params.Path,
		Content: params.Content,
	}

	resp, err := bundle.Sandbox.Write(ctx, req)
	if err != nil {
		return nil, err
	}

	// F-11: do NOT journal raw `params.Content`. Every dfmt_write of a
	// secrets-laden file (env, config, key) would otherwise land verbatim in
	// the journal — only pattern-redacted, not sanitized. A truncated SHA-256
	// plus byte count keeps the audit trail (same write detectable across
	// time) without exposing the payload.
	sum := sha256.Sum256([]byte(params.Content))
	h.logEvent(ctx, "tool.write", params.Path, map[string]any{
		"path":          params.Path,
		"bytes":         len(params.Content),
		"content_sha16": hex.EncodeToString(sum[:8]),
	})

	return &WriteResponse{
		Success: resp.Success,
		Summary: resp.Summary,
	}, nil
}
