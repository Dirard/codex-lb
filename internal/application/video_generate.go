package application

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"codex-lb/internal/domain"
)

const (
	videoModelClassic    = "grok-imagine-video"
	videoModelFull       = "grok-imagine-video-1.5"
	videoModelLite       = "grok-imagine-video-1.5-lite"
	videoDefaultSeconds  = 8
	videoMaxSeconds      = 15
	videoMaxKeyframes    = 4
	videoMaxReferences   = 14
	videoMaxAudios       = 3
	videoMaxPromptChars  = 32000
	videoMaxAttempts     = 4
	videoMaxUnfinished   = 4
	videoDefaultCooldown = time.Minute
	videoFirstPollDelay  = 10 * time.Second
	videoErrorBytes      = 500

	videoStatusUnauthorized    = 401
	videoStatusForbidden       = 403
	videoStatusNotFound        = 404
	videoStatusTooManyRequests = 429
)

// VideoPoolExhausted reports that every tried account refused or is cooling.
type VideoPoolExhausted struct{ RetryAfter time.Duration }

func (e *VideoPoolExhausted) Error() string { return "The video pool has no capacity now" }

// VideoGeneration is the validated form of a create request.
type VideoGeneration struct {
	Body    json.RawMessage
	Model   string
	Mode    string
	Seconds int
}

var videoFields = map[string]bool{"model": true, "prompt": true, "image": true, "last_frame": true, "keyframes": true,
	"reference_images": true, "reference_audios": true, "duration": true, "aspect_ratio": true, "resolution": true}

var videoAspectRatios = map[string]bool{"1:1": true, "16:9": true, "9:16": true, "4:3": true, "3:4": true, "3:2": true, "2:3": true, "21:9": true, "5:2": true}

var videoResolutions = map[string]bool{"480p": true, "720p": true, "1080p": true}

func videoInvalid(param, message string) error {
	return &ProxyError{Code: "invalid_request", Status: 400, Message: message, Param: param}
}

// videoImageRef accepts {"url": "..."} only: file ids belong to one xAI
// account and cannot move across the pool.
func videoImageRef(raw json.RawMessage) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || len(object) != 1 {
		return false
	}
	var url string
	return json.Unmarshal(object["url"], &url) == nil && strings.TrimSpace(url) != ""
}

// validateVideoRequest rejects a body before any job row or upstream call.
func validateVideoRequest(body json.RawMessage) (VideoGeneration, error) {
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return VideoGeneration{}, videoInvalid("", "The request must be a JSON object")
	}
	for name := range object {
		if !videoFields[name] {
			return VideoGeneration{}, videoInvalid(name, "This field is not supported")
		}
	}
	var model string
	if json.Unmarshal(object["model"], &model) != nil || model != videoModelClassic && model != videoModelFull && model != videoModelLite {
		return VideoGeneration{}, videoInvalid("model", "The model must be grok-imagine-video, grok-imagine-video-1.5 or grok-imagine-video-1.5-lite")
	}
	seconds := videoDefaultSeconds
	if raw, ok := object["duration"]; ok {
		var duration float64
		if json.Unmarshal(raw, &duration) != nil || duration != math.Trunc(duration) || duration < 1 || duration > videoMaxSeconds {
			return VideoGeneration{}, videoInvalid("duration", "The duration must be an integer from 1 to 15 seconds")
		}
		seconds = int(duration)
	}
	prompt := ""
	if raw, ok := object["prompt"]; ok && (json.Unmarshal(raw, &prompt) != nil || utf8.RuneCountInString(prompt) > videoMaxPromptChars) {
		return VideoGeneration{}, videoInvalid("prompt", "The prompt must be a string of at most 32000 characters")
	}
	for _, name := range []string{"image", "last_frame"} {
		if raw, ok := object[name]; ok && !videoImageRef(raw) {
			return VideoGeneration{}, videoInvalid(name, "The image needs one url: a public URL or a base64 data URL")
		}
	}
	var keyframes []struct {
		Image     json.RawMessage `json:"image"`
		Timestamp *float64        `json:"timestamp_s"`
	}
	if raw, ok := object["keyframes"]; ok && (json.Unmarshal(raw, &keyframes) != nil || len(keyframes) == 0 || len(keyframes) > videoMaxKeyframes) {
		return VideoGeneration{}, videoInvalid("keyframes", "Keyframes must be a list of 1 to 4 entries")
	}
	for _, keyframe := range keyframes {
		if !videoImageRef(keyframe.Image) || keyframe.Timestamp == nil || *keyframe.Timestamp <= 0 || *keyframe.Timestamp >= float64(seconds) {
			return VideoGeneration{}, videoInvalid("keyframes", "Each keyframe needs an image url and a timestamp_s strictly inside the duration")
		}
	}
	var references []json.RawMessage
	if raw, ok := object["reference_images"]; ok && (json.Unmarshal(raw, &references) != nil || len(references) == 0 || len(references) > videoMaxReferences) {
		return VideoGeneration{}, videoInvalid("reference_images", "Reference images must be a list of 1 to 14 entries")
	}
	for _, reference := range references {
		if !videoImageRef(reference) {
			return VideoGeneration{}, videoInvalid("reference_images", "Each reference image needs one url")
		}
	}
	var audios []map[string]json.RawMessage
	if raw, ok := object["reference_audios"]; ok && (json.Unmarshal(raw, &audios) != nil || len(audios) == 0 || len(audios) > videoMaxAudios) {
		return VideoGeneration{}, videoInvalid("reference_audios", "Reference audios must be a list of 1 to 3 entries")
	}
	for _, audio := range audios {
		var url, voice string
		_ = json.Unmarshal(audio["url"], &url)
		_ = json.Unmarshal(audio["voice_id"], &voice)
		if len(audio) != 1 || url == "" && voice == "" {
			return VideoGeneration{}, videoInvalid("reference_audios", "Each reference audio needs a url or a voice_id")
		}
	}
	for name, allowed := range map[string]map[string]bool{"aspect_ratio": videoAspectRatios, "resolution": videoResolutions} {
		var value string
		if raw, ok := object[name]; ok && (json.Unmarshal(raw, &value) != nil || !allowed[value]) {
			return VideoGeneration{}, videoInvalid(name, "This value is not supported")
		}
	}
	_, hasImage := object["image"]
	_, hasLast := object["last_frame"]
	pinned := hasImage || hasLast || len(keyframes) != 0
	if !pinned && strings.TrimSpace(prompt) == "" {
		return VideoGeneration{}, videoInvalid("prompt", "A prompt is required unless a frame is pinned")
	}
	if model != videoModelFull {
		for name, used := range map[string]bool{"last_frame": hasLast, "keyframes": len(keyframes) != 0, "reference_images": len(references) != 0, "reference_audios": len(audios) != 0} {
			if used {
				return VideoGeneration{}, videoInvalid(name, "This field needs grok-imagine-video-1.5")
			}
		}
	}
	mode := "text"
	switch {
	case len(references) != 0 || len(audios) != 0:
		mode = "references"
	case len(keyframes) != 0:
		mode = "keyframes"
	case hasLast:
		mode = "first_last_frame"
	case hasImage:
		mode = "first_frame"
	}
	return VideoGeneration{Body: body, Model: model, Mode: mode, Seconds: seconds}, nil
}

// Generate creates one video on the least busy account. A refusal proved to
// precede execution moves the same body to another account; an attempt that
// may have executed is never replayed.
func (s *VideoService) Generate(ctx context.Context, keyID string, body json.RawMessage) (VideoJobView, error) {
	generation, err := validateVideoRequest(body)
	if err != nil {
		return VideoJobView{}, err
	}
	release, err := s.admission.Acquire(ctx)
	if err != nil {
		return VideoJobView{}, err
	}
	defer release()
	excluded := map[string]bool{}
	for attempt := 0; attempt < videoMaxAttempts; attempt++ {
		now := s.now().UTC()
		job := domain.VideoJob{ID: videoID("vid_"), APIKeyID: keyID, Model: generation.Model, Mode: generation.Mode,
			RequestedSeconds: generation.Seconds, Status: domain.VideoJobDispatching, CreatedAt: now}
		account, err := s.store.ReserveVideoJob(ctx, job, excluded, videoMaxUnfinished, now)
		if errors.Is(err, domain.ErrNoAccounts) {
			return VideoJobView{}, s.noVideoAccount(ctx, len(excluded) != 0)
		}
		if err != nil {
			return VideoJobView{}, err
		}
		job.AccountID = account.ID
		view, retry, err := s.dispatchVideo(ctx, job, generation)
		if !retry {
			return view, err
		}
		excluded[account.ID] = true
	}
	return VideoJobView{}, s.noVideoAccount(ctx, true)
}

func (s *VideoService) noVideoAccount(ctx context.Context, refused bool) error {
	now := s.now()
	until, err := s.store.NearestVideoCooldown(ctx, now)
	if err != nil {
		return err
	}
	if until != nil {
		return &VideoPoolExhausted{RetryAfter: until.Sub(now)}
	}
	if refused {
		return &VideoPoolExhausted{RetryAfter: videoDefaultCooldown}
	}
	return &ProxyError{Code: "video_no_accounts", Status: 503, Message: "No video account is available now"}
}

// dispatchVideo runs one attempt. retry means this account refused before
// execution and the request may try another one.
func (s *VideoService) dispatchVideo(ctx context.Context, job domain.VideoJob, generation VideoGeneration) (VideoJobView, bool, error) {
	token, version, err := s.accessToken(ctx, job.AccountID, -1)
	if err != nil {
		// Nothing was sent, so this account can simply be skipped.
		return VideoJobView{}, true, s.finishAttempt(ctx, job.ID, domain.VideoJobRefused, "video_token_unavailable")
	}
	response, err := s.upstream.CreateVideo(ctx, token, generation.Body)
	if err == nil && response.Status == videoStatusUnauthorized {
		token, _, err = s.accessToken(ctx, job.AccountID, version)
		if err != nil {
			return VideoJobView{}, true, s.finishAttempt(ctx, job.ID, domain.VideoJobRefused, "video_token_unavailable")
		}
		response, err = s.upstream.CreateVideo(ctx, token, generation.Body)
	}
	if err != nil {
		return VideoJobView{}, false, s.videoMayHaveExecuted(ctx, job.ID, err)
	}
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	now := s.now().UTC()
	switch {
	case response.Status >= 200 && response.Status < 300:
		var created struct {
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(response.Body, &created) != nil || created.RequestID == "" {
			return VideoJobView{}, false, s.videoMayHaveExecuted(ctx, job.ID, errors.New("invalid upstream response"))
		}
		if err := s.store.MarkVideoJobPending(writeCtx, job.ID, created.RequestID, now.Add(videoFirstPollDelay)); err != nil {
			return VideoJobView{}, false, &ProxyError{Code: "video_job_persistence_failed", Status: 503, Message: "The video job could not be recorded"}
		}
		job.Status = domain.VideoJobPending
		return viewVideoJob(job, nil), false, nil
	case response.Status == videoStatusUnauthorized:
		if err := s.store.SetVideoAccountStatus(writeCtx, job.AccountID, domain.VideoAccountReauthRequired, "http_401", now); err != nil {
			return VideoJobView{}, false, err
		}
		return VideoJobView{}, true, s.finishAttempt(ctx, job.ID, domain.VideoJobRefused, "http_401")
	case response.Status == videoStatusForbidden:
		if err := s.store.SetVideoAccountStatus(writeCtx, job.AccountID, domain.VideoAccountAccessDenied, "http_403", now); err != nil {
			return VideoJobView{}, false, err
		}
		return VideoJobView{}, true, s.finishAttempt(ctx, job.ID, domain.VideoJobRefused, "http_403")
	case response.Status == videoStatusTooManyRequests:
		// An exhausted pool is known from its reading, so a 429 without
		// Retry-After is treated as a short rate limit, such as too many jobs at
		// once, and does not take the account out for long.
		cooldown := response.RetryAfter
		if cooldown <= 0 {
			cooldown = videoDefaultCooldown
		}
		if err := s.store.SetVideoCooldown(writeCtx, job.AccountID, "http_429", now.Add(cooldown), now); err != nil {
			return VideoJobView{}, false, err
		}
		return VideoJobView{}, true, s.finishAttempt(ctx, job.ID, domain.VideoJobRefused, "http_429")
	case response.Status >= 400 && response.Status < 500:
		if err := s.finishAttempt(ctx, job.ID, domain.VideoJobRejected, "video_request_rejected"); err != nil {
			return VideoJobView{}, false, err
		}
		return VideoJobView{}, false, &ProxyError{Code: "video_request_rejected", Status: response.Status, Message: videoUpstreamMessage(response.Body)}
	default:
		return VideoJobView{}, false, s.videoMayHaveExecuted(ctx, job.ID, errors.New("upstream server error"))
	}
}

func (s *VideoService) finishAttempt(ctx context.Context, id string, status domain.VideoJobStatus, code string) error {
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	_, err := s.store.FinishVideoJob(writeCtx, id, domain.VideoJobFinish{Status: status, ErrorCode: code, At: s.now().UTC()})
	return err
}

// videoMayHaveExecuted ends an attempt whose outcome is unknown: it may be
// generating at xAI with no id to read it by, so it is neither replayed nor
// reported as a refusal.
func (s *VideoService) videoMayHaveExecuted(ctx context.Context, id string, cause error) error {
	if err := s.finishAttempt(ctx, id, domain.VideoJobUnknown, "video_result_unknown"); err != nil {
		return err
	}
	if errors.Is(cause, context.Canceled) {
		return cause
	}
	return &ProxyError{Code: "video_upstream_error", Status: 502, Message: "xAI did not confirm the video job"}
}

// videoUpstreamMessage keeps xAI's refusal reason, which is about the request
// the client just sent, and bounds it.
func videoUpstreamMessage(body []byte) string {
	var payload struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if message := videoErrorText(payload.Error); message != "" {
			return message
		}
	}
	return "xAI rejected this request"
}

// videoErrorText reads xAI's `error`, a string or an object with a message,
// and bounds it; it is empty when xAI gave no words.
func videoErrorText(raw json.RawMessage) string {
	var message string
	if json.Unmarshal(raw, &message) != nil {
		var object struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &object) != nil {
			return ""
		}
		message = object.Message
	}
	if len(message) > videoErrorBytes {
		message = strings.ToValidUTF8(message[:videoErrorBytes], "")
	}
	return message
}
