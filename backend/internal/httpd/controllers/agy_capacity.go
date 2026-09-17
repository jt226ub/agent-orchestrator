package controllers

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
)

// AgyCapacityService is the HTTP controller's Antigravity plan-capacity
// boundary. Capacity is advisory: it is shown, never used for admission.
type AgyCapacityService interface {
	CachedAgyCapacity(context.Context) (domain.AgyCapacitySnapshot, error)
	EnsureAgyCapacity(context.Context, bool) (domain.AgyCapacitySnapshot, error)
}

// AgyCapacityController exposes the cached Antigravity capacity snapshot and
// an on-demand refresh.
type AgyCapacityController struct{ Svc AgyCapacityService }

// Register adds request-timeout-bound Antigravity capacity routes.
func (c *AgyCapacityController) Register(r chi.Router) {
	r.Get("/agents/agy/capacity", c.get)
	r.Post("/agents/agy/capacity/ensure", c.ensure)
}

func (c *AgyCapacityController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/agy/capacity")
		return
	}
	result, err := c.Svc.CachedAgyCapacity(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyCapacityResponse(result))
}

func (c *AgyCapacityController) ensure(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/capacity/ensure")
		return
	}
	var request EnsureAgyCapacityRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.EnsureAgyCapacity(r.Context(), request.Force)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyCapacityResponse(result))
}
