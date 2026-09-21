//nolint:dupl // Antigravity and Claude Code keep separate usage contracts by design (FORK.md).
package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// ClaudeCodeUsageService is the HTTP controller's Claude Code plan-usage
// boundary. Usage is advisory: it is shown, never used for admission.
type ClaudeCodeUsageService interface {
	CachedClaudeCodeUsage(context.Context) (domain.ClaudeCodePlanUsageSnapshot, error)
	EnsureClaudeCodeUsage(context.Context, bool) (domain.ClaudeCodePlanUsageSnapshot, error)
}

// ClaudeCodeUsageController exposes the cached Claude Code plan-usage
// snapshot and an on-demand refresh.
type ClaudeCodeUsageController struct{ Svc ClaudeCodeUsageService }

// Register adds request-timeout-bound Claude Code plan-usage routes.
func (c *ClaudeCodeUsageController) Register(r chi.Router) {
	r.Get("/agents/claude-code/usage", c.get)
	r.Post("/agents/claude-code/usage/ensure", c.ensure)
}

func (c *ClaudeCodeUsageController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/claude-code/usage")
		return
	}
	result, err := c.Svc.CachedClaudeCodeUsage(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newClaudeCodeUsageResponse(result))
}

func (c *ClaudeCodeUsageController) ensure(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/claude-code/usage/ensure")
		return
	}
	var request EnsureClaudeCodeUsageRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.EnsureClaudeCodeUsage(r.Context(), request.Force)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newClaudeCodeUsageResponse(result))
}
