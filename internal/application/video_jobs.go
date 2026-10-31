package application

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"codex-lb/internal/domain"
)

const (
	videoPollSoonFor        = 2 * time.Minute
	videoPollLaterDelay     = time.Minute
	videoPendingDeadline    = time.Hour
	videoDispatchDeadline   = 15 * time.Minute
	videoJobRetention       = 90 * 24 * time.Hour
	videoSweepBatch         = 20
	videoSweepConcurrency   = 4
	videoPruneBatch         = 500
	videoTicksPerMicrodolar = 10_000
	videoMaxPageSize        = 100
)

// VideoJobView is the job as the REST surface shows it: it never names the
// account or the upstream request.
type VideoJobView struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Progress   *float64        `json:"progress,omitempty"`
	Model      string          `json:"model"`
	Mode       string          `json:"mode"`
	CreatedAt  time.Time       `json:"created_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
	Video      *VideoJobResult `json:"video,omitempty"`
	CostUSD    *float64        `json:"cost_usd,omitempty"`
	Error      string          `json:"error,omitempty"`
	// ErrorMessage says in words why a finished job gave no video.
	ErrorMessage string `json:"error_message,omitempty"`
}

type VideoJobResult struct {
	URL      string   `json:"url"`
	Duration *float64 `json:"duration,omitempty"`
}

func viewVideoJob(job domain.VideoJob, progress *float64) VideoJobView {
	view := VideoJobView{ID: job.ID, Status: string(job.Status), Progress: progress, Model: job.Model, Mode: job.Mode,
		CreatedAt: job.CreatedAt, FinishedAt: job.FinishedAt, Error: job.ErrorCode,
		ErrorMessage: videoFailureReason(job.ErrorCode, job.ErrorMessage)}
	if job.Status == domain.VideoJobDispatching {
		view.Status = string(domain.VideoJobPending)
	}
	if job.VideoURL != "" {
		view.Video = &VideoJobResult{URL: job.VideoURL, Duration: job.DurationSeconds}
	}
	if job.CostMicrodollars != nil {
		cost := float64(*job.CostMicrodollars) / 1e6
		view.CostUSD = &cost
	}
	return view
}

// videoFailureReason prefers xAI's own words and otherwise explains the code,
// which also covers jobs finished before the reason was stored.
func videoFailureReason(code, stored string) string {
	if stored != "" {
		return stored
	}
	switch code {
	case "":
		return ""
	case "video_moderated":
		return "xAI blocked this video by moderation"
	case "video_failed":
		return "xAI could not generate this video"
	case "video_expired":
		return "xAI expired this job before it gave a video"
	case "video_result_unknown":
		return "xAI gave no result in time, so the outcome of this job is unknown"
	case "video_account_deleted":
		return "The Grok account of this job was removed, so its result cannot be read"
	case "video_request_rejected":
		return "xAI rejected this request"
	case "video_token_unavailable":
		return "The Grok account could not be authorized"
	case "http_401", "http_403":
		return "xAI refused the Grok account"
	case "http_429":
		return "xAI refused the job because of a rate limit"
	}
	return "The video job ended without a video: " + code
}

func videoJobNotFound() error {
	return &ProxyError{Code: "video_job_not_found", Status: 404, Message: "No such video job"}
}

// Status returns the key's job, reading xAI only while the job is unfinished.
func (s *VideoService) Status(ctx context.Context, keyID, id string) (VideoJobView, error) {
	job, err := s.store.GetVideoJob(ctx, id)
	if errors.Is(err, domain.ErrNotFound) || err == nil && (job.APIKeyID != keyID || job.Status == domain.VideoJobRefused) {
		return VideoJobView{}, videoJobNotFound()
	}
	if err != nil {
		return VideoJobView{}, err
	}
	if job.Status != domain.VideoJobPending {
		return viewVideoJob(job, nil), nil
	}
	return s.pollVideoJob(ctx, job)
}

// pollVideoJob reads one pending job at its own account and applies at most
// one transition. A failed read leaves the job pending for the next poll.
func (s *VideoService) pollVideoJob(ctx context.Context, job domain.VideoJob) (VideoJobView, error) {
	token, version, err := s.accessToken(ctx, job.AccountID, -1)
	if err != nil {
		return viewVideoJob(job, nil), s.keepVideoJobPending(ctx, job, 0, "token_unavailable")
	}
	response, err := s.upstream.GetVideo(ctx, token, job.UpstreamRequestID)
	if err == nil && response.Status == videoStatusUnauthorized {
		if token, _, err = s.accessToken(ctx, job.AccountID, version); err != nil {
			return viewVideoJob(job, nil), s.keepVideoJobPending(ctx, job, 0, "token_unavailable")
		}
		response, err = s.upstream.GetVideo(ctx, token, job.UpstreamRequestID)
	}
	if err != nil {
		return viewVideoJob(job, nil), s.keepVideoJobPending(ctx, job, 0, "read_failed")
	}
	now := s.now().UTC()
	var payload struct {
		Status   string   `json:"status"`
		Progress *float64 `json:"progress"`
		Video    struct {
			URL               string   `json:"url"`
			Duration          *float64 `json:"duration"`
			RespectModeration *bool    `json:"respect_moderation"`
		} `json:"video"`
		Usage struct {
			Ticks *int64 `json:"cost_in_usd_ticks"`
		} `json:"usage"`
		Error json.RawMessage `json:"error"`
	}
	decoded := json.Unmarshal(response.Body, &payload) == nil
	finish := domain.VideoJobFinish{At: now}
	switch {
	case response.Status == videoStatusNotFound:
		finish.Status, finish.ErrorCode = domain.VideoJobExpired, "video_expired"
	case response.Status >= 300 || !decoded:
		return viewVideoJob(job, nil), s.keepVideoJobPending(ctx, job, response.Status, "unexpected_answer")
	case payload.Status == "done" && payload.Video.RespectModeration != nil && !*payload.Video.RespectModeration:
		finish.Status, finish.ErrorCode = domain.VideoJobFailed, "video_moderated"
	case payload.Status == "done" && payload.Video.URL == "":
		finish.Status, finish.ErrorCode, finish.ErrorMessage = domain.VideoJobFailed, "video_failed", "xAI finished this job without a video"
	case payload.Status == "done":
		finish.Status, finish.VideoURL, finish.DurationSeconds = domain.VideoJobDone, payload.Video.URL, payload.Video.Duration
		if payload.Usage.Ticks != nil && *payload.Usage.Ticks >= 0 {
			cost := *payload.Usage.Ticks / videoTicksPerMicrodolar
			finish.CostMicrodollars = &cost
		}
	case payload.Status == "failed":
		finish.Status, finish.ErrorCode, finish.ErrorMessage = domain.VideoJobFailed, "video_failed", videoErrorText(payload.Error)
	case payload.Status == "expired":
		finish.Status, finish.ErrorCode, finish.ErrorMessage = domain.VideoJobExpired, "video_expired", videoErrorText(payload.Error)
	default:
		return viewVideoJob(job, payload.Progress), s.keepVideoJobPending(ctx, job, response.Status, videoStatusWord(payload.Status))
	}
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	if _, err := s.store.FinishVideoJob(writeCtx, job.ID, finish); err != nil {
		return VideoJobView{}, err
	}
	finished, err := s.store.GetVideoJob(writeCtx, job.ID)
	if err != nil {
		return VideoJobView{}, err
	}
	return viewVideoJob(finished, nil), nil
}

// keepVideoJobPending moves a due job to its next poll and leaves a trace when
// it is taking long. A poll that is not due, such as a client polling between
// sweeps, changes and logs nothing, which bounds the warnings per job.
func (s *VideoService) keepVideoJobPending(ctx context.Context, job domain.VideoJob, httpStatus int, word string) error {
	now := s.now().UTC()
	if job.NextPollAt != nil && now.Before(*job.NextPollAt) {
		return nil
	}
	if age := now.Sub(job.CreatedAt); age > videoPollSoonFor {
		s.log.Warn("video job is still unfinished", "job", job.ID, "age_seconds", int(age.Seconds()),
			"upstream_http_status", httpStatus, "upstream_status", word)
	}
	return s.scheduleVideoPoll(ctx, job, now)
}

// videoStatusWord keeps provider text out of the log.
func videoStatusWord(status string) string {
	switch status {
	case "pending", "queued", "processing", "in_progress":
		return status
	case "done":
		return "done_without_result"
	case "":
		return "none"
	}
	return "other"
}

func (s *VideoService) scheduleVideoPoll(ctx context.Context, job domain.VideoJob, now time.Time) error {
	delay := videoFirstPollDelay
	if now.Sub(job.CreatedAt) > videoPollSoonFor {
		delay = videoPollLaterDelay
	}
	writeCtx, done := videoWriteContext(ctx)
	defer done()
	return s.store.ScheduleVideoPoll(writeCtx, job.ID, now.Add(delay))
}

// VideoJobPage is one page of jobs with the cursor of the next one.
type VideoJobPage struct {
	Jobs []VideoJobView         `json:"jobs"`
	Next *domain.VideoJobCursor `json:"-"`
}

// List pages the key's own jobs from their rows, without calling xAI.
func (s *VideoService) List(ctx context.Context, keyID string, before *domain.VideoJobCursor, limit int) (VideoJobPage, error) {
	limit = min(max(limit, 1), videoMaxPageSize)
	jobs, err := s.store.ListVideoJobs(ctx, keyID, before, limit+1)
	if err != nil {
		return VideoJobPage{}, err
	}
	page := VideoJobPage{Jobs: make([]VideoJobView, 0, min(len(jobs), limit))}
	for index, job := range jobs {
		if index == limit {
			last := jobs[limit-1]
			page.Next = &domain.VideoJobCursor{CreatedAt: last.CreatedAt, ID: last.ID}
			break
		}
		page.Jobs = append(page.Jobs, viewVideoJob(job, nil))
	}
	return page, nil
}

// Sweep finishes jobs nobody polls: it ends stale jobs, reads the due ones with
// bounded fan-out, joins them and keeps every result of a partial failure.
func (s *VideoService) Sweep(ctx context.Context) error {
	now := s.now().UTC()
	if _, err := s.store.ExpireStaleVideoJobs(ctx, now.Add(-videoPendingDeadline), now.Add(-videoDispatchDeadline), now); err != nil {
		return err
	}
	if _, err := s.store.PruneVideoJobs(ctx, now.Add(-videoJobRetention), videoPruneBatch); err != nil {
		return err
	}
	jobs, err := s.store.DueVideoJobs(ctx, now, videoSweepBatch)
	if err != nil {
		return err
	}
	slots := make(chan struct{}, videoSweepConcurrency)
	errs := make([]error, len(jobs))
	var group sync.WaitGroup
	for index, job := range jobs {
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			group.Wait()
			return ctx.Err()
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-slots }()
			_, errs[index] = s.pollVideoJob(ctx, job)
		}()
	}
	group.Wait()
	return errors.Join(errs...)
}

// RunSweep polls jobs until the context ends; the runtime cancels and joins it.
func (s *VideoService) RunSweep(ctx context.Context, schedule BackgroundSchedule) {
	RunPeriodic(ctx, "video_jobs", schedule, videoFirstPollDelay, nil, s.Sweep)
}

// RunUsagePoller keeps the pool readings fresh; the runtime cancels and joins it.
func (s *VideoService) RunUsagePoller(ctx context.Context, schedule BackgroundSchedule) {
	RunPeriodic(ctx, "video_usage", schedule, videoUsageInterval, nil, s.RefreshUsage)
}

// VideoSummary holds the totals of the dashboard page.
type VideoSummary struct {
	Today  domain.VideoPeriodTotals
	Week   domain.VideoPeriodTotals
	Month  domain.VideoPeriodTotals
	AsOfAt time.Time
}

func (s *VideoService) Summary(ctx context.Context) (VideoSummary, error) {
	now := s.now().UTC()
	summary := VideoSummary{AsOfAt: now}
	var err error
	if summary.Today, err = s.store.VideoTotalsSince(ctx, now.Truncate(24*time.Hour)); err != nil {
		return summary, err
	}
	if summary.Week, err = s.store.VideoTotalsSince(ctx, now.Add(-7*24*time.Hour)); err != nil {
		return summary, err
	}
	summary.Month, err = s.store.VideoTotalsSince(ctx, now.Add(-30*24*time.Hour))
	return summary, err
}

// Jobs pages the whole pool's jobs for the dashboard.
func (s *VideoService) Jobs(ctx context.Context, before *domain.VideoJobCursor, limit int) ([]domain.VideoJobEntry, *domain.VideoJobCursor, error) {
	limit = min(max(limit, 1), videoMaxPageSize)
	entries, err := s.store.VideoJobEntries(ctx, before, limit+1)
	if err != nil {
		return nil, nil, err
	}
	for index := range entries {
		entries[index].ErrorMessage = videoFailureReason(entries[index].ErrorCode, entries[index].ErrorMessage)
	}
	if len(entries) <= limit {
		return entries, nil, nil
	}
	last := entries[limit-1]
	return entries[:limit], &domain.VideoJobCursor{CreatedAt: last.CreatedAt, ID: last.ID}, nil
}
