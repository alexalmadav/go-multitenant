package httpmw

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/alexalmadav/go-multitenant/tenant"
	"github.com/google/uuid"
)

// DefaultErrorHandler writes a JSON error response with a status derived
// from the error's code. Applications can replace it via Config.ErrorHandler.
func DefaultErrorHandler(w http.ResponseWriter, r *http.Request, err error) {
	status := http.StatusInternalServerError
	body := map[string]interface{}{}

	var tenantErr *tenant.TenantError
	var validationErr *tenant.ValidationError
	switch {
	case errors.As(err, &tenantErr):
		status = statusForCode(tenantErr.Code)
		body["error"] = map[string]string{"code": tenantErr.Code, "message": tenantErr.Message}
		if tenantErr.TenantID != uuid.Nil {
			body["tenant_id"] = tenantErr.TenantID.String()
		}
	case errors.As(err, &validationErr):
		status = http.StatusBadRequest
		body["error"] = map[string]string{"code": "VALIDATION_ERROR", "message": validationErr.Message, "field": validationErr.Field}
	default:
		body["error"] = map[string]string{"code": "INTERNAL_ERROR", "message": "An internal error occurred"}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func statusForCode(code string) int {
	switch code {
	case "TENANT_NOT_FOUND":
		return http.StatusNotFound
	case "TENANT_SUSPENDED", "TENANT_CANCELLED", "TENANT_PENDING", "TENANT_INVALID_STATUS":
		return http.StatusForbidden
	case "PLAN_LIMIT_EXCEEDED":
		return http.StatusPaymentRequired
	case "PLAN_NOT_CONFIGURED":
		return http.StatusForbidden
	default:
		return http.StatusInternalServerError
	}
}
