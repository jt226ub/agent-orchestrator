package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apispec"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	"github.com/aoagents/agent-orchestrator/backend/internal/ports"
	agentsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/agent"
)

// AgyAccountService is the HTTP controller's account-management boundary.
type AgyAccountService interface {
	CachedAgyAccounts(context.Context) (agentsvc.AgyAccounts, error)
	EnsureAgyAccounts(context.Context, []string, agentsvc.AgyAccountEnsureOptions) (agentsvc.AgyAccounts, error)
	SubscribeAgyAccounts(context.Context) (<-chan agentsvc.AgyAccounts, error)
	OpenAgyAccountLoginTerminal(context.Context) (agentsvc.AgyAccountLoginTerminalStart, error)
	OpenAgyAccountReauthenticationTerminal(context.Context, string) (agentsvc.AgyAccountLoginTerminalStart, error)
	LogoutAgyAccount(context.Context, string) (agentsvc.AgyAccounts, error)
	DeleteAgyAccount(context.Context, string) (agentsvc.AgyAccounts, error)
	VerifyAgyAccountLogin(context.Context, string) (domain.AgyAccountLoginOperation, error)
	CancelAgyAccountLogin(context.Context, string) (domain.AgyAccountLoginOperation, error)
	StartAgyAccountSwitch(context.Context, ports.AgyAccountSwitchConfig) (domain.AgyAccountSwitch, error)
	GetAgyAccountSwitch(context.Context, string) (domain.AgyAccountSwitch, error)
}

// AgyAccountsController exposes cached accounts, login, switching, and events.
type AgyAccountsController struct{ Svc AgyAccountService }

// Register adds request-timeout-bound Agy account routes.
func (c *AgyAccountsController) Register(r chi.Router) {
	r.Get("/agents/agy/accounts", c.list)
	r.Post("/agents/agy/accounts/ensure", c.ensure)
	r.Post("/agents/agy/accounts/{accountId}/login-terminal", c.openReauthenticationTerminal)
	r.Post("/agents/agy/accounts/{accountId}/logout", c.logoutAccount)
	r.Delete("/agents/agy/accounts/{accountId}", c.deleteAccount)
	r.Post("/agents/agy/accounts/login-terminal", c.openLoginTerminal)
	r.Post("/agents/agy/accounts/login-operations/{operationId}/verify", c.verifyLogin)
	r.Post("/agents/agy/accounts/login-operations/{operationId}/cancel", c.cancelLogin)
	r.Post("/agents/agy/account-switches", c.startSwitch)
	r.Get("/agents/agy/account-switches/{switchId}", c.getSwitch)
}

func (c *AgyAccountsController) getSwitch(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/agy/account-switches/{switchId}")
		return
	}
	result, err := c.Svc.GetAgyAccountSwitch(r.Context(), strings.TrimSpace(chi.URLParam(r, "switchId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgySwitchResponse(result))
}

func (c *AgyAccountsController) startSwitch(w http.ResponseWriter, r *http.Request) { //nolint:dupl // Codex and Antigravity keep separate account contracts by design (FORK.md).
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/account-switches")
		return
	}
	var request StartAgyAccountSwitchRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(request.IdempotencyKey) == "" {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "IDEMPOTENCY_KEY_REQUIRED", "Idempotency key is required", nil)
		return
	}
	result, err := c.Svc.StartAgyAccountSwitch(r.Context(), ports.AgyAccountSwitchConfig{
		TargetAccountID: request.TargetAccountID, IdempotencyKey: request.IdempotencyKey,
	})
	if err != nil {
		writeAgyAccountSwitchError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusAccepted, newAgySwitchResponse(result))
}

func writeAgyAccountSwitchError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrAgyAccountAlreadyActive):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_ACCOUNT_ALREADY_ACTIVE", "This Antigravity account is already active", nil)
	case errors.Is(err, ports.ErrAgyAccountSwitchInProgress):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_ACCOUNT_SWITCH_IN_PROGRESS", "An Antigravity account switch is already in progress", nil)
	case errors.Is(err, ports.ErrAgyAccountSwitchIdempotencyConflict):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_ACCOUNT_SWITCH_IDEMPOTENCY_CONFLICT", "The idempotency key belongs to another Antigravity account switch", nil)
	case errors.Is(err, ports.ErrAgyGlobalCredentialStoreUnsupported):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_GLOBAL_CREDENTIAL_STORE_UNSUPPORTED", "Device-global Antigravity account switching requires file-backed credentials", nil)
	case errors.Is(err, ports.ErrAgyGlobalAccountChanged):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_GLOBAL_ACCOUNT_CHANGED", "The device Antigravity account changed during switching", nil)
	case errors.Is(err, ports.ErrAgyAccountLoginInProgress):
		envelope.WriteAPIError(w, r, http.StatusConflict, "conflict", "AGY_ACCOUNT_LOGIN_IN_PROGRESS", "Finish or close the Antigravity account login before switching accounts", nil)
	default:
		envelope.WriteError(w, r, err)
	}
}

// RegisterStreams adds the long-lived Agy account event route.
func (c *AgyAccountsController) RegisterStreams(r chi.Router) {
	r.Get("/agents/agy/accounts/events", c.events)
}

func (c *AgyAccountsController) list(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/agy/accounts")
		return
	}
	result, err := c.Svc.CachedAgyAccounts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyAccountsResponse(result))
}

func (c *AgyAccountsController) ensure(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/ensure")
		return
	}
	var request EnsureAgyAccountsRequest
	if err := decodeJSONStrict(r, &request); err != nil {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_JSON", "Invalid JSON body", nil)
		return
	}
	result, err := c.Svc.EnsureAgyAccounts(r.Context(), request.AccountIDs, agentsvc.AgyAccountEnsureOptions{
		ForceAuthentication:       request.ForceAuthentication,
		ForceDeviceReconciliation: request.ForceDeviceReconciliation,
	})
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyAccountsResponse(result))
}

func (c *AgyAccountsController) openLoginTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/login-terminal")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.OpenAgyAccountLoginTerminal(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeAgyLoginTerminal(w, result)
}

func (c *AgyAccountsController) openReauthenticationTerminal(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/{accountId}/login-terminal")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.OpenAgyAccountReauthenticationTerminal(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	writeAgyLoginTerminal(w, result)
}

func writeAgyLoginTerminal(w http.ResponseWriter, result agentsvc.AgyAccountLoginTerminalStart) {
	envelope.WriteJSON(w, http.StatusAccepted, OpenAgyAccountLoginTerminalResponse{
		Operation: newAgyLoginResponse(result.Operation),
		ShellTerminal: AgyAccountLoginTerminalResponse{
			HandleID:  result.ShellTerminal.HandleID,
			Title:     result.ShellTerminal.Title,
			CreatedAt: result.ShellTerminal.CreatedAt,
		},
	})
}

func (c *AgyAccountsController) logoutAccount(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/{accountId}/logout")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.LogoutAgyAccount(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyAccountsResponse(result))
}

func (c *AgyAccountsController) deleteAccount(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "DELETE", "/api/v1/agents/agy/accounts/{accountId}")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1))
	if err != nil || len(body) != 0 {
		envelope.WriteAPIError(w, r, http.StatusBadRequest, "bad_request", "INVALID_REQUEST_BODY", "Request body must be empty", nil)
		return
	}
	result, err := c.Svc.DeleteAgyAccount(r.Context(), strings.TrimSpace(chi.URLParam(r, "accountId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyAccountsResponse(result))
}

func (c *AgyAccountsController) verifyLogin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/login-operations/{operationId}/verify")
		return
	}
	result, err := c.Svc.VerifyAgyAccountLogin(r.Context(), strings.TrimSpace(chi.URLParam(r, "operationId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyLoginResponse(result))
}

func (c *AgyAccountsController) cancelLogin(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "POST", "/api/v1/agents/agy/accounts/login-operations/{operationId}/cancel")
		return
	}
	result, err := c.Svc.CancelAgyAccountLogin(r.Context(), strings.TrimSpace(chi.URLParam(r, "operationId")))
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	envelope.WriteJSON(w, http.StatusOK, newAgyLoginResponse(result))
}

func (c *AgyAccountsController) events(w http.ResponseWriter, r *http.Request) {
	if c.Svc == nil {
		apispec.NotImplemented(w, r, "GET", "/api/v1/agents/agy/accounts/events")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		envelope.WriteAPIError(w, r, http.StatusInternalServerError, "internal", "SSE_UNSUPPORTED", "Streaming is not supported by this server", nil)
		return
	}
	events, err := c.Svc.SubscribeAgyAccounts(r.Context())
	if err != nil {
		envelope.WriteError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case event, ok := <-events:
			if !ok {
				return
			}
			data, err := json.Marshal(newAgyAccountsResponse(event))
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "event: agy_account\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
