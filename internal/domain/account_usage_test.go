package domain

import (
	"testing"
	"time"
)

func TestPurchasedCreditEvidence(t *testing.T) {
	yes, no := true, false
	positive, zero := 12.5, 0.0
	now := time.Now().UTC()
	for _, test := range []struct {
		name   string
		credit AccountCreditStatus
		usable bool
	}{
		{"unknown", AccountCreditStatus{}, false},
		{"has only", AccountCreditStatus{Has: &yes}, true},
		{"none", AccountCreditStatus{Has: &no}, false},
		{"positive", AccountCreditStatus{Balance: &positive}, true},
		{"explicit zero wins", AccountCreditStatus{Has: &yes, Balance: &zero}, false},
		{"unlimited wins", AccountCreditStatus{Unlimited: &yes, Balance: &zero}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			credit := test.credit
			credit.ObservedAt = now
			before, after := now.Add(-time.Second), now.Add(time.Second)
			if credit.Usable() != test.usable || credit.UsableAfter(nil) != test.usable || credit.UsableAfter(&before) != test.usable {
				t.Fatal("incorrect available-credit decision")
			}
			if credit.UsableAfter(&now) || credit.UsableAfter(&after) {
				t.Fatal("old credits overrode a confirmed upstream refusal")
			}
		})
	}
}
