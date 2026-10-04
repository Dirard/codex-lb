package application

import (
	"context"
	"time"

	"codex-lb/internal/domain"
)

type Accounts interface {
	SaveAccount(context.Context, domain.Account) error
	GetAccount(context.Context, string) (domain.Account, error)
	ListAccounts(context.Context) ([]domain.Account, error)
	DeleteAccount(context.Context, string, bool) error
	SaveAccountCredential(context.Context, domain.AccountCredential) error
	GetAccountCredential(context.Context, string) (domain.AccountCredential, error)
	SaveAccountQuota(context.Context, domain.AccountQuota) error
	ListAccountQuota(context.Context, string) ([]domain.AccountQuota, error)
	RecordAccountOutcome(context.Context, string, string, bool, bool) error
}

type AccountQuotaMetadata interface {
	LoadAccountCreditStatus(context.Context, string) (*domain.AccountCreditStatus, error)
	ListAccountAdditionalQuotas(context.Context, string) ([]domain.AccountAdditionalQuota, error)
	LoadAccountQuotaRefusalAt(context.Context, string) (*time.Time, error)
}

type Groups interface {
	SaveGroup(context.Context, domain.AccountGroup, time.Time) error
	GetGroup(context.Context, string) (domain.AccountGroup, error)
	ListGroups(context.Context) ([]domain.AccountGroup, error)
	DeleteGroup(context.Context, string) error
	ResetGroupUsage(context.Context, string, time.Time) error
	SetAccountGroups(context.Context, string, []string) error
}

type APIKeys interface {
	SaveAPIKey(context.Context, domain.APIKey, time.Time) error
	GetAPIKey(context.Context, string) (domain.APIKey, error)
	FindAPIKeyByHash(context.Context, string) (domain.APIKey, error)
	ListAPIKeys(context.Context) ([]domain.APIKey, error)
	DeleteAPIKey(context.Context, string) error
	ResetAPIKeyUsage(context.Context, string, time.Time) error
	EligibleAccounts(context.Context, string) ([]domain.Account, error)
}

type UsageLedger interface {
	ReserveUsage(context.Context, domain.ReservationRequest) (domain.Reservation, error)
	SettleUsage(context.Context, string, domain.UsageSettlement) (bool, error)
	GetReservation(context.Context, string) (domain.Reservation, error)
	ListReservationsNeedingReconciliation(context.Context, string, int) ([]domain.Reservation, error)
	TouchReservation(context.Context, string, time.Time) (bool, error)
	MarkReservationUncertain(context.Context, string) (bool, error)
	ReleaseStaleReservations(context.Context, time.Time) (int, error)
	RecordUsage(context.Context, domain.UsageEvent) (bool, error)
	UsageTotals(context.Context, string, string) (domain.UsageTotals, error)
}

type Settings interface {
	LoadSettings(context.Context) (domain.RuntimeSettings, error)
	SaveSettings(context.Context, domain.RuntimeSettings) error
	LoadAdminSecret(context.Context) (domain.AdminSecret, error)
	SaveAdminSecret(context.Context, domain.AdminSecret) error
	InitializeAdminSecret(context.Context, domain.AdminSecret) (bool, error)
	AdvanceTOTPStep(context.Context, int64) (bool, error)
}
