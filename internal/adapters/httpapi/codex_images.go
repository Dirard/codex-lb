package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"codex-lb/internal/application"
	"codex-lb/internal/domain"
)

const (
	maxImageFormFiles      = 16
	maxImageFormTextFields = 32
	maxImageFormTextBytes  = 256 << 10
)

// registerImageRoutes serves Codex's `$imagegen` (`{base_url}/images/*` under
// the Codex base) and the OpenAI-compatible `/v1/images/*` routes.
func (h *CodexOperationsHandler) registerImageRoutes(mux *http.ServeMux) {
	for _, prefix := range []string{"/backend-api/codex/images/", "/v1/images/"} {
		for _, operation := range []string{"generations", "edits"} {
			edit := operation == "edits"
			handler := func(w http.ResponseWriter, r *http.Request) { h.images(w, r, edit) }
			mux.HandleFunc("POST "+prefix+operation, handler)
			mux.HandleFunc("POST "+prefix+operation+"/{$}", handler)
		}
	}
}

func (h *CodexOperationsHandler) images(w http.ResponseWriter, r *http.Request, edit bool) {
	keyID, err := h.authenticate(r)
	if err != nil {
		writeProxyError(w, err)
		return
	}
	lease := h.bodies.lease(keyID)
	defer lease.release()
	var body json.RawMessage
	if edit && strings.HasPrefix(r.URL.Path, "/v1/") && mediaType(r.Header.Get("Content-Type")) == "multipart/form-data" {
		body, err = parseImageEditForm(w, r, lease)
	} else {
		body, err = readOperationJSON(w, r, lease, domain.MaxRequestBodyBytes)
	}
	if err != nil {
		writeProxyError(w, err)
		return
	}
	stream := imageEventWriter{w: w, controller: http.NewResponseController(w)}
	result, err := h.ops.ImagesWithOptions(r.Context(), h.operationOptions(r, keyID),
		application.CodexImageRequest{Edit: edit, Body: body, Emit: stream.write})
	if stream.started {
		if err != nil {
			// The response already started: report the failure in-band.
			_, code, message := proxyError(err)
			payload, _ := json.Marshal(map[string]any{"type": "error", "error": map[string]string{"code": code, "message": message}})
			_ = stream.write(application.CodexImageEvent{Name: "error", Data: payload})
		}
		return
	}
	if err == nil && result.Status == http.StatusTooManyRequests {
		// A final image-limit refusal keeps the upstream reset headers so the
		// client can tell when the limit resets.
		for name, values := range domain.ImageLimitHeaders(result.Headers) {
			w.Header()[name] = values
		}
	}
	h.writeResult(w, result, err, false)
}

// imageEventWriter relays upstream image events as text/event-stream, keeping
// each event name and data payload unchanged.
type imageEventWriter struct {
	w          http.ResponseWriter
	controller *http.ResponseController
	started    bool
}

func (s *imageEventWriter) write(event application.CodexImageEvent) error {
	if !s.started {
		s.w.Header().Set("Content-Type", "text/event-stream")
		s.w.Header().Set("Cache-Control", "no-cache, no-store")
		s.w.Header().Set("X-Accel-Buffering", "no")
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	_ = s.controller.SetWriteDeadline(time.Now().Add(downstreamWriteTimeout))
	var frame strings.Builder
	if event.Name != "" {
		frame.WriteString("event: " + event.Name + "\n")
	}
	for _, line := range strings.Split(string(event.Data), "\n") {
		frame.WriteString("data: " + line + "\n")
	}
	frame.WriteString("\n")
	if _, err := io.WriteString(s.w, frame.String()); err != nil {
		return err
	}
	return s.controller.Flush()
}

// parseImageEditForm converts an OpenAI multipart image edit into the JSON
// shape of the subscription endpoint: files become base64 data URLs in
// images[].image_url and mask.image_url, numeric and boolean fields become
// JSON values. File bytes are charged to the body lease as they arrive.
func parseImageEditForm(w http.ResponseWriter, r *http.Request, lease *bodyLease) (json.RawMessage, error) {
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || params["boundary"] == "" {
		return nil, &application.ProxyError{Code: "invalid_content_type", Status: 415, Message: "Content-Type must be multipart/form-data with a boundary"}
	}
	switch strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))) {
	case "", "identity":
	default:
		return nil, &application.ProxyError{Code: "unsupported_content_encoding", Status: 415, Message: "Unsupported request encoding"}
	}
	source, err := lease.open(w, r, domain.MaxRequestBodyBytes)
	if err != nil {
		return nil, err
	}
	reader := multipart.NewReader(newChargedReader(r, source, lease, domain.MaxRequestBodyBytes, false), params["boundary"])
	object := map[string]any{}
	var images []map[string]string
	var mask map[string]string
	fields := 0
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if failure := bodyFailure(err); failure != nil {
			return nil, failure
		}
		if err != nil {
			return nil, imageFormInvalid("", "Invalid multipart request")
		}
		name := part.FormName()
		switch {
		case name == "image" || name == "image[]" || name == "mask":
			url, err := imageDataURL(part, name)
			if err != nil {
				return nil, err
			}
			if name == "mask" {
				if mask != nil {
					return nil, imageFormInvalid("mask", "Only one mask is allowed")
				}
				mask = map[string]string{"image_url": url}
				continue
			}
			if len(images) == maxImageFormFiles {
				return nil, imageFormInvalid("image", "An edit accepts at most 16 images")
			}
			images = append(images, map[string]string{"image_url": url})
		case part.FileName() != "":
			return nil, imageFormInvalid(name, "Unsupported file field")
		default:
			fields++
			if fields > maxImageFormTextFields {
				return nil, imageFormInvalid("", "Too many form fields")
			}
			value, err := io.ReadAll(io.LimitReader(part, maxImageFormTextBytes+1))
			if failure := bodyFailure(err); failure != nil {
				return nil, failure
			}
			if err != nil || len(value) > maxImageFormTextBytes {
				return nil, imageFormInvalid(name, "Invalid form field")
			}
			converted, err := imageFormValue(name, string(value))
			if err != nil {
				return nil, err
			}
			object[name] = converted
		}
	}
	if len(images) != 0 {
		object["images"] = images
	}
	if mask != nil {
		object["mask"] = mask
	}
	return json.Marshal(object)
}

func imageDataURL(part *multipart.Part, name string) (string, error) {
	param := "image"
	if name == "mask" {
		param = "mask"
	}
	data, err := io.ReadAll(io.LimitReader(part, domain.MaxRequestBodyBytes+1))
	if failure := bodyFailure(err); failure != nil {
		return "", failure
	}
	if err != nil || len(data) == 0 {
		return "", imageFormInvalid(param, "Invalid image file")
	}
	kind := mediaType(part.Header.Get("Content-Type"))
	if !supportedImageType(kind) {
		kind = mediaType(http.DetectContentType(data))
	}
	if !supportedImageType(kind) {
		return "", imageFormInvalid(param, "Images must be PNG, JPEG or WebP")
	}
	return "data:" + kind + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func supportedImageType(kind string) bool {
	return kind == "image/png" || kind == "image/jpeg" || kind == "image/webp"
}

func imageFormValue(name, value string) (any, error) {
	switch name {
	case "n", "output_compression", "partial_images":
		number, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, imageFormInvalid(name, name+" must be an integer")
		}
		return number, nil
	case "stream":
		flag, err := strconv.ParseBool(strings.TrimSpace(value))
		if err != nil {
			return nil, imageFormInvalid(name, "stream must be a boolean")
		}
		return flag, nil
	default:
		return value, nil
	}
}

func imageFormInvalid(param, message string) error {
	return &application.ProxyError{Code: "invalid_request", Status: 400, Message: message, Param: param}
}
