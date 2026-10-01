package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

func TestProxyRetainsOnlyUnprovenCapacityOrCancellationUsage(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		for _, beforeCreate := range []bool{false, true} {
			name := "capacity"
			if cancelled {
				name = "cancellation"
			}
			if beforeCreate {
				name += "_not_sent"
			} else {
				name += "_dispatched"
			}
			t.Run(name, func(t *testing.T) {
				failure := &application.ProviderFailure{Code: "local_capacity_exceeded", Status: 503, Dispatched: true, RejectedBeforeExecution: beforeCreate}
				var callErr error = failure
				if cancelled {
					failure.Code, failure.Status = "request_cancelled", 499
					callErr = errors.Join(failure, context.Canceled)
				}
				calls := 0
				server, store := wireFixture(t, wireProvider(func(context.Context, application.ResponseTarget, json.RawMessage, func(application.ResponseEvent) error) (application.ResponseResult, error) {
					calls++
					return application.ResponseResult{}, callErr
				}))
				request, _ := http.NewRequest("POST", server.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-6-luna","input":"synthetic","stream":true}`))
				request.Header.Set("Authorization", "Bearer synthetic-key")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if response.StatusCode != failure.Status || calls != 1 {
					t.Fatal("refusal changed status or retried generation")
				}
				ctx := context.Background()
				pending, err := store.ListReservationsNeedingReconciliation(ctx, "", 10)
				if err != nil {
					t.Fatal(err)
				}
				key, err := store.GetAPIKey(ctx, "wire-key")
				if err != nil {
					t.Fatal(err)
				}
				logs, err := store.ListRequestLogs(ctx, domain.RequestLogFilter{APIKeyIDs: []string{"wire-key"}, Limit: 10})
				if err != nil || logs.Total != 1 {
					t.Fatal("request outcome missing", err)
				}
				if beforeCreate {
					if len(pending) != 0 || key.Limits[0].CurrentValue != 0 || logs.Requests[0].ErrorCode == nil || *logs.Requests[0].ErrorCode != failure.Code {
						t.Fatal("proven unsent call kept its reservation or lost its actual cause")
					}
				} else if len(pending) != 1 || key.Limits[0].CurrentValue == 0 || logs.Requests[0].Tokens != nil || logs.Requests[0].CostUSD != nil {
					t.Fatal("unknown dispatched usage was released or fabricated")
				}
			})
		}
	}
}
