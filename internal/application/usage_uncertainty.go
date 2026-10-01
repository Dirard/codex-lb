package application

import (
	"context"
	"errors"
	"time"
)

// callSafely converts provider panics into sanitized uncertain outcomes before settlement.
func callSafely[T any](call func() (T, error)) (result T, err error) {
	defer func() {
		if recover() != nil {
			err = &ProviderFailure{Code: "upstream_adapter_failure", Status: 502, Dispatched: true}
		}
	}()
	return call()
}

func operationUsageUncertain(status int, usageKnown bool, callErr error) bool {
	if usageKnown {
		return false
	}
	var failure *ProviderFailure
	if errors.As(callErr, &failure) {
		return failure.Dispatched && !failure.RejectedBeforeExecution
	}
	if callErr != nil {
		return true // Cancellation is not proof that an already-started call was free.
	}
	return status == 0 || status == 408 || status >= 500 || status >= 200 && status < 300
}

// retainUncertainUsage keeps only this request's existing reservation until
// explicit reconciliation. It does not invent a zero-cost completed event.
func retainUncertainUsage(ctx context.Context, ledger interface {
	MarkReservationUncertain(context.Context, string) (bool, error)
}, id string) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	marked, err := ledger.MarkReservationUncertain(cleanup, id)
	if err != nil || !marked {
		return &ProxyError{Code: "usage_settlement_failed", Status: 503, Message: "Unknown operation usage could not be retained for reconciliation"}
	}
	return nil
}
