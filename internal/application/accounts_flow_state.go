package application

import (
	"context"
	"time"
)

func (s *AccountsService) flowByStateLocked(state string) *oauthFlow {
	if state == "" {
		return nil
	}
	return s.flows.flows[s.flows.byState[state]]
}

func (s *AccountsService) rememberFlowLocked(flow *oauthFlow) {
	if s.flows.flows == nil {
		s.flows.flows = make(map[string]*oauthFlow)
		s.flows.byState = make(map[string]string)
	}
	s.flows.flows[flow.id] = flow
	if flow.stateToken != "" {
		s.flows.byState[flow.stateToken] = flow.id
	}
	s.flows.latest = flow.id
	if flow.method == "browser" && flow.status == "pending" {
		s.flows.pendingBrowser++
	}
	flow.expiryTimer = time.AfterFunc(max(time.Millisecond, flow.expiresAt.Sub(s.now())), func() { s.recordFlowError(flow, "Login flow expired.") })
}

func (s *AccountsService) pruneExpiredLocked(now time.Time) {
	for _, flow := range s.flows.flows {
		if flow.status == "pending" && now.After(flow.expiresAt) {
			if flow.pollCancel != nil {
				flow.pollCancel()
			}
			s.finishFlowLocked(flow, "error", "Login flow expired.")
		}
	}
}

func (s *AccountsService) removeFlowLocked(flow *oauthFlow) {
	if flow.expiryTimer != nil {
		flow.expiryTimer.Stop()
	}
	delete(s.flows.flows, flow.id)
	if flow.stateToken != "" {
		delete(s.flows.byState, flow.stateToken)
	}
	if s.flows.latest == flow.id {
		s.flows.latest = ""
	}
	if flow.method == "browser" && flow.status == "pending" {
		s.flows.pendingBrowser--
	}
}

func (s *AccountsService) recordFlowError(flow *oauthFlow, message string) {
	s.finishFlow(flow, "error", message)
}

func (s *AccountsService) finishFlow(flow *oauthFlow, status, message string) {
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.finishFlowLocked(flow, status, message)
}

func (s *AccountsService) finishFlowLocked(flow *oauthFlow, status, message string) {
	if flow.status != "pending" || s.flows.flows[flow.id] != flow {
		return
	}
	if flow.method == "device" && s.flows.deviceCurrent != flow.id {
		return
	}
	flow.status = status
	flow.errorMessage = message
	flow.finishedAt = s.now()
	if flow.expiryTimer != nil {
		flow.expiryTimer.Stop()
	}
	if flow.method == "browser" {
		s.flows.pendingBrowser--
		if s.flows.pendingBrowser == 0 {
			s.stopCallbackServerLocked()
		}
	}
	s.flows.terminal++
	for s.flows.terminal > maxTerminalFlows {
		var oldest *oauthFlow
		for _, candidate := range s.flows.flows {
			if candidate.status == "pending" {
				continue
			}
			if oldest == nil || candidate.finishedAt.Before(oldest.finishedAt) {
				oldest = candidate
			}
		}
		if oldest == nil {
			break
		}
		s.removeFlowLocked(oldest)
		s.flows.terminal--
	}
}

func (s *AccountsService) startCallbackServerLocked() {
	if s.flows.callbackServer != nil || s.callback == nil {
		return
	}
	server, err := s.callback(func(ctx context.Context, callbackURL string) OAuthStatusResult {
		return s.ManualCallback(ctx, callbackURL, "")
	})
	if err != nil {
		return // manual-callback still completes the flow
	}
	s.flows.callbackServer = server
}

func (s *AccountsService) stopCallbackServerLocked() {
	if s.flows.callbackServer == nil {
		return
	}
	server := s.flows.callbackServer
	s.flows.callbackServer = nil
	_ = server.Close()
}

func (s *AccountsService) pruneExpiredPublic() {
	s.flows.mu.Lock()
	defer s.flows.mu.Unlock()
	s.pruneExpiredLocked(s.now())
}
