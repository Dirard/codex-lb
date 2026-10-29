package application

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"codex-lb/internal/domain"
	"codex-lb/internal/domain/pricing"
)

const (
	// ImageDefaultModel is the image model of a request that names none; Codex
	// always sends it.
	ImageDefaultModel       = "gpt-image-2"
	imageKindGeneration     = "image_generation"
	imageKindEdit           = "image_edit"
	maxImagePromptChars     = 32000
	maxImageInputs          = 16
	maxImageModelBytes      = 128
	maxImageCount           = 10
	imageCooldownDuration   = 10 * time.Minute
	maxImageAccountAttempts = 4
	imageOutputAllowance    = 8192
	imageInputAllowance     = 1500
)

// CodexImageRequest is a validated Images request forwarded unchanged to the
// subscription images endpoint.
type CodexImageRequest struct {
	Edit bool
	Body json.RawMessage
}

func imageRequestKind(kind string) bool { return kind == imageKindGeneration || kind == imageKindEdit }

// imageCooldowns keeps accounts whose image limit refused a request out of
// image routing for a while. It is per process and never affects other routes.
type imageCooldowns struct {
	mu    sync.Mutex
	until map[string]time.Time
}

func (c *imageCooldowns) cool(accountID string, until time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.until == nil {
		c.until = make(map[string]time.Time)
	}
	c.until[accountID] = until
}

func (c *imageCooldowns) active(now time.Time) map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[string]bool, len(c.until))
	for id, until := range c.until {
		if !until.After(now) {
			delete(c.until, id)
			continue
		}
		result[id] = true
	}
	return result
}

// ImagesWithOptions forwards one image generation or edit to a subscription
// account. The reservation is settled once per attempt; only an image-limit
// refusal before any output moves the request to another account.
func (s *CodexOperations) ImagesWithOptions(ctx context.Context, options CodexOperationOptions, request CodexImageRequest) (CodexOperationResult, error) {
	key, err := s.activeKey(ctx, options.KeyID)
	if err != nil {
		return CodexOperationResult{}, err
	}
	body, model, err := prepareImageRequest(request.Body, request.Edit, key)
	if err != nil {
		return CodexOperationResult{}, err
	}
	request.Body = body
	kind := imageKindGeneration
	if request.Edit {
		kind = imageKindEdit
	}
	fileOwner, _, err := s.singleFileOwner(ctx, key.ID, nil, body)
	if err != nil {
		return CodexOperationResult{}, err
	}
	started := s.now()
	release, err := s.acquire(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	defer release()
	call := func(_ string, target CodexOperationTarget) (CodexOperationResult, error) {
		return s.provider.Images(ctx, target, request)
	}
	if fileOwner != nil {
		// File references are account-bound: the edit never leaves the owner.
		account, err := s.strictOwnerAccount(ctx, key.ID, fileOwner.AccountID, fileOwner.AccountGeneration, fileOwner.RouteRevision)
		if err != nil {
			return CodexOperationResult{}, err
		}
		if account.Kind != domain.AccountChatGPT {
			return CodexOperationResult{}, ownerUnavailable()
		}
		target := CodexOperationTarget{Account: account, KeyID: key.ID, ConfirmedOwner: true, accountNeutral: true}
		return s.billedAdmitted(ctx, options, key, account, model, kind, target, call, nil, body, true, started)
	}
	if s.selectCompact == nil {
		return CodexOperationResult{}, &ProxyError{Code: "image_routing_unavailable", Status: 503, Message: "Image account selection is not configured"}
	}
	settings, err := s.store.LoadSettings(ctx)
	if err != nil {
		return CodexOperationResult{}, err
	}
	selection := responseRequest{Model: model, ImageGeneration: true}
	cooled := s.imageCooldowns.active(s.now())
	refusedHere := map[string]bool{}
	var last CodexOperationResult
	var lastErr error
	for attempt := 0; attempt < maxImageAccountAttempts; attempt++ {
		account, lease, err := s.selectCompact(ctx, key.ID, &selection, settings, domain.Account{}, unionAccounts(cooled, refusedHere))
		if err != nil && len(cooled) != 0 {
			// Every candidate is cooling down: ask upstream again rather than
			// answer with a local outage the client would retry.
			cooled = nil
			account, lease, err = s.selectCompact(ctx, key.ID, &selection, settings, domain.Account{}, refusedHere)
		}
		if err != nil {
			if len(refusedHere) != 0 {
				return last, lastErr
			}
			return CodexOperationResult{}, err
		}
		target := CodexOperationTarget{Account: account, KeyID: key.ID, admissionLease: lease, accountNeutral: true}
		result, callErr := s.billedAdmitted(ctx, options, key, account, model, kind, target, call, nil, body, false, started)
		if !compactQuotaRefusal(result, callErr) || result.OutputObserved || result.UsageReported {
			return result, callErr
		}
		s.imageCooldowns.cool(account.ID, s.now().Add(imageCooldownDuration))
		refusedHere[account.ID] = true
		last, lastErr = result, callErr
		if ctx.Err() != nil {
			break
		}
	}
	return last, lastErr
}

func unionAccounts(sets ...map[string]bool) map[string]bool {
	result := map[string]bool{}
	for _, set := range sets {
		for id := range set {
			result[id] = true
		}
	}
	return result
}

// prepareImageRequest validates an Images body before any reservation and
// returns the body to forward and the effective public model. Raw values are
// kept as sent; the body is re-encoded only when the model changes.
func prepareImageRequest(body json.RawMessage, edit bool, key domain.APIKey) (json.RawMessage, string, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return nil, "", imageInvalid("", "Image request must be a JSON object")
	}
	var prompt string
	if raw, ok := object["prompt"]; !ok || json.Unmarshal(raw, &prompt) != nil || strings.TrimSpace(prompt) == "" {
		return nil, "", imageInvalid("prompt", "A non-empty prompt is required")
	}
	if utf8.RuneCountInString(prompt) > maxImagePromptChars {
		return nil, "", imageInvalid("prompt", "The prompt exceeds 32000 characters")
	}
	model, rewrite := ImageDefaultModel, true
	if raw, ok := object["model"]; ok && !jsonNull(raw) {
		if json.Unmarshal(raw, &model) != nil {
			return nil, "", imageInvalid("model", "The image model must be a string")
		}
		rewrite = false
	}
	if key.EnforcedModel != nil && isImageModel(*key.EnforcedModel) && *key.EnforcedModel != model {
		model, rewrite = *key.EnforcedModel, true
	}
	if !isImageModel(model) {
		return nil, "", imageInvalid("model", "Image generation requires a supported gpt-image model")
	}
	if len(key.AllowedModels) != 0 && !modelAllowed(key.AllowedModels, model) {
		return nil, "", &ProxyError{Code: "model_not_allowed", Status: 403, Message: "This key does not allow the requested image model"}
	}
	if raw, ok := object["n"]; ok && !jsonNull(raw) {
		var n float64
		if json.Unmarshal(raw, &n) != nil || n != math.Trunc(n) || n < 1 || n > maxImageCount {
			return nil, "", imageInvalid("n", "n must be an integer from 1 to 10")
		}
	}
	if raw, ok := object["stream"]; ok && !jsonNull(raw) {
		var stream bool
		if json.Unmarshal(raw, &stream) != nil {
			return nil, "", imageInvalid("stream", "stream must be a boolean")
		}
		if stream {
			return nil, "", imageInvalid("stream", "Streaming image responses are not supported")
		}
	}
	if edit {
		var images []map[string]json.RawMessage
		if json.Unmarshal(object["images"], &images) != nil || len(images) == 0 || len(images) > maxImageInputs {
			return nil, "", imageInvalid("images", "An edit needs 1 to 16 images")
		}
		for _, image := range images {
			if !imageReference(image) {
				return nil, "", imageInvalid("images", "Each image needs an image_url or file_id")
			}
		}
		if raw, ok := object["mask"]; ok && !jsonNull(raw) {
			var mask map[string]json.RawMessage
			if json.Unmarshal(raw, &mask) != nil || !imageReference(mask) {
				return nil, "", imageInvalid("mask", "The mask needs an image_url or file_id")
			}
		}
	}
	if !rewrite {
		return body, model, nil
	}
	encodedModel, err := json.Marshal(model)
	if err != nil {
		return nil, "", err
	}
	object["model"] = encodedModel
	forwarded, err := json.Marshal(object)
	if err != nil {
		return nil, "", imageInvalid("", "Image request must be a JSON object")
	}
	return forwarded, model, nil
}

func imageReference(object map[string]json.RawMessage) bool {
	for _, field := range []string{"image_url", "file_id"} {
		var value string
		if raw, ok := object[field]; ok && json.Unmarshal(raw, &value) == nil && strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func isImageModel(model string) bool {
	if len(model) > maxImageModelBytes || !strings.HasPrefix(strings.ToLower(model), "gpt-image-") {
		return false
	}
	_, _, err := pricing.LookupCodex(model)
	return err == nil
}

func jsonNull(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

func imageInvalid(param, message string) error {
	return &ProxyError{Code: "invalid_request", Status: 400, Message: message, Param: param}
}

// imageBudget reserves the prompt, a fixed allowance per input image and an
// image-sized output instead of counting base64 image bytes as tokens.
func imageBudget(body []byte, price pricing.Price) domain.UsageAmount {
	var request struct {
		Prompt string            `json:"prompt"`
		Images []json.RawMessage `json:"images"`
	}
	_ = json.Unmarshal(body, &request)
	input := int64(len(request.Prompt)/4) + 1 + int64(len(request.Images))*imageInputAllowance
	budget := domain.UsageAmount{InputTokens: input, OutputTokens: imageOutputAllowance}
	budget.CostMicrodollars, _ = price.Cost(budget, "")
	return budget
}

// diagnosticRequest keeps image payloads out of error diagnostics; the other
// request fields stay available for diagnosis.
func diagnosticRequest(kind string, body []byte) []byte {
	if !imageRequestKind(kind) {
		return body
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil {
		return nil
	}
	var images []json.RawMessage
	_ = json.Unmarshal(object["images"], &images)
	delete(object, "images")
	if _, ok := object["mask"]; ok {
		object["mask"] = json.RawMessage(`"omitted"`)
	}
	if len(images) != 0 {
		count, _ := json.Marshal(len(images))
		object["images_omitted"] = count
	}
	summary, err := json.Marshal(object)
	if err != nil {
		return nil
	}
	return summary
}
