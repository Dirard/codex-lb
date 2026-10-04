package application

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"codex-lb/internal/domain"
)

func TestRuntimeDiscoveryPreservesDurableUpdateFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "successful check", true: "failed check"}[fail], func(t *testing.T) {
			failure := "Failed startup; previous version restored"
			state := domain.RuntimeInstallState{Current: domain.RuntimeInstallation{Descriptor: testDescriptor("go-v1.0.0")}, Phase: "failed", LastError: failure}
			source := &fakeReleaseSource{release: domain.RuntimeRelease{Version: "go-v1.0.1"}}
			if fail {
				source.latestErr = errors.New("network failure")
			}
			service := NewRuntimeUpdates(context.Background(), source, &fakeInstallStore{}, &fakeUpdateHost{}, state, RuntimeUpdateConfig{})
			defer service.Close()
			if _, err := service.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				status, _ := service.Status(context.Background())
				if status.Phase != "checking" {
					if status.Phase != "failed" || !strings.Contains(status.LastError, failure) {
						t.Fatalf("discovery hid the failed update: %+v", status)
					}
					if !fail && (!status.UpdateAvailable || status.CheckedAt == nil) {
						t.Fatal("preserving the failure prevented release discovery")
					}
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("discovery did not complete")
		})
	}
}
