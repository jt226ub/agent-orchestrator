package controllers

import (
	"context"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	profilessvc "github.com/aoagents/agent-orchestrator/backend/internal/service/profiles"
)

// DefaultProfilesService is the controller-facing contract for the user-level
// profiles document and the daemon-wide rules files.
type DefaultProfilesService interface {
	Get(ctx context.Context) (profilessvc.Snapshot, error)
	SetProfiles(ctx context.Context, profiles map[string]domain.RoleProfile) (profilessvc.Snapshot, error)
	ReadRules(ctx context.Context, name string) (profilessvc.RulesFile, error)
	WriteRules(ctx context.Context, name, content string) (profilessvc.RulesFile, error)
	DeleteRules(ctx context.Context, name string) error
}

// DefaultProfilesController owns the daemon-owned profile and rules routes
// under /settings, beside the other daemon-owned preferences.
type DefaultProfilesController struct{ Svc DefaultProfilesService }

// Register mounts the default profile and rules file routes.
func (c *DefaultProfilesController) Register(r chi.Router) {
	r.Get("/settings/profiles", c.get)
	r.Put("/settings/profiles", c.setProfiles)
	r.Get("/settings/rules/{name}", c.readRules)
	r.Put("/settings/rules/{name}", c.writeRules)
	r.Delete("/settings/rules/{name}", c.deleteRules)
}

func (c *DefaultProfilesController) get(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/settings/profiles")
		return
	}
	snapshot, err := c.Svc.Get(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newDefaultProfilesResponse(snapshot))
}

func (c *DefaultProfilesController) setProfiles(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/settings/profiles")
		return
	}
	var req UpdateDefaultProfilesRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	snapshot, err := c.Svc.SetProfiles(r.Context(), req.Profiles)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newDefaultProfilesResponse(snapshot))
}

func (c *DefaultProfilesController) readRules(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/settings/rules/{name}")
		return
	}
	file, err := c.Svc.ReadRules(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newRulesFileResponse(file))
}

func (c *DefaultProfilesController) writeRules(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "PUT", "/api/v1/settings/rules/{name}")
		return
	}
	var req UpdateRulesFileRequest
	if err := decodeJSONStrict(r, &req); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if req.Content == nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "validation", "RULES_CONTENT_REQUIRED", "content is required", nil)
		return
	}
	file, err := c.Svc.WriteRules(r.Context(), chi.URLParam(r, "name"), *req.Content)
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newRulesFileResponse(file))
}

func (c *DefaultProfilesController) deleteRules(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/settings/rules/{name}")
		return
	}
	if err := c.Svc.DeleteRules(r.Context(), chi.URLParam(r, "name")); err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func newDefaultProfilesResponse(snapshot profilessvc.Snapshot) DefaultProfilesResponse {
	profiles := snapshot.Profiles
	if profiles == nil {
		profiles = map[string]domain.RoleProfile{}
	}
	files := make([]RulesFileSummaryResponse, len(snapshot.RulesFiles))
	for i, file := range snapshot.RulesFiles {
		files[i] = RulesFileSummaryResponse{Name: file.Name, SizeBytes: file.SizeBytes, UpdatedAt: file.UpdatedAt}
	}
	return DefaultProfilesResponse{Profiles: profiles, RulesFiles: files}
}

func newRulesFileResponse(file profilessvc.RulesFile) RulesFileResponse {
	return RulesFileResponse{Name: strings.TrimSpace(file.Name), Content: file.Content, UpdatedAt: file.UpdatedAt}
}
