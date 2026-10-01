package sqlite

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"codex-lb/internal/domain"
)

type accountAdmissionEnvironment struct {
	create, stream, reserve, fairShare int
}

func loadAccountAdmissionEnvironment() (accountAdmissionEnvironment, error) {
	values := accountAdmissionEnvironment{}
	for _, field := range []struct {
		name         string
		defaultValue int
		dst          *int
		maximum      int
	}{
		{"CODEX_LB_PROXY_ACCOUNT_RESPONSE_CREATE_LIMIT", 4, &values.create, 0},
		{"CODEX_LB_PROXY_ACCOUNT_STREAM_LIMIT", 8, &values.stream, 0},
		{"CODEX_LB_PROXY_ACCOUNT_STREAM_RECOVERY_RESERVE", 1, &values.reserve, 0},
		{"CODEX_LB_PROXY_API_KEY_FAIR_SHARE_CONGESTION_THRESHOLD_PCT", 0, &values.fairShare, 100},
	} {
		*field.dst = field.defaultValue
		if raw, present := os.LookupEnv(field.name); present {
			value, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || value < 0 || field.maximum > 0 && value > field.maximum {
				return values, fmt.Errorf("invalid %s: %w", field.name, domain.ErrInvalid)
			}
			*field.dst = value
		}
	}
	if values.stream > 0 && values.reserve > values.stream {
		return values, fmt.Errorf("stream recovery reserve exceeds stream cap: %w", domain.ErrInvalid)
	}
	return values, nil
}

func effectiveAccountAdmission(v *int, environment int) int {
	if v != nil {
		return *v
	}
	return environment
}
