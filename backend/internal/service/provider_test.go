package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

const testReferenceImageDataURL = "data:image/png;base64,aGVsbG8="

func TestSystemChannelIDFromBaseURLOnlyAcceptsRelativeProxyPath(t *testing.T) {
	if got := systemChannelIDFromBaseURL("/api/ai/system/channel-1"); got != "channel-1" {
		t.Fatalf("relative system proxy path = %q", got)
	}
	for _, value := range []string{
		"https://attacker.example/api/ai/system/channel-1",
		"https://attacker.example/prefix/api/ai/system/channel-1",
		"api/ai/system/channel-1",
		"/api/ai/system/channel-1/other",
		"/api/ai/system/channel-1?redirect=https://attacker.example",
	} {
		if got := systemChannelIDFromBaseURL(value); got != "" {
			t.Fatalf("systemChannelIDFromBaseURL(%q) = %q, want empty", value, got)
		}
	}
}

func TestPostJSONRejectsUnserializableBodyBeforeSupplierCall(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	var target map[string]interface{}
	err := postJSON(context.Background(), providerConfig{BaseURL: server.URL, APIKey: "secret"}, "/v1/test", map[string]interface{}{"invalid": func() {}}, &target)
	if err == nil || calls != 0 {
		t.Fatalf("postJSON() error = %v, supplier calls = %d", err, calls)
	}
	if message := taskFailureMessage(err); message != providerRequestSerializationFailureMessage {
		t.Fatalf("taskFailureMessage() = %q", message)
	}
	if billingFailureUncertain(err) {
		t.Fatalf("serialization before request must remain refundable: %v", err)
	}
}

func TestWriteMediaPartSanitizesFilenameAndSetsMimeType(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writeMediaPart(writer, "image", providerMedia{ID: "image-1", Name: "提示词\n带换行.png", Type: "image/png", DataURL: testReferenceImageDataURL}); err != nil {
		t.Fatalf("writeMediaPart() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart.Writer.Close() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://example.test", bytes.NewReader(body.Bytes()))
	request.Header.Set("Content-Type", writer.FormDataContentType())
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		t.Fatalf("ParseMultipartForm() error = %v", err)
	}
	files := request.MultipartForm.File["image"]
	if len(files) != 1 {
		t.Fatalf("image files = %d, want 1", len(files))
	}
	file := files[0]
	if file.Filename != "reference-image-1.png" || strings.ContainsAny(file.Filename, "\r\n") {
		t.Fatalf("filename = %q", file.Filename)
	}
	if got := file.Header.Get("Content-Type"); got != "image/png" {
		t.Fatalf("part Content-Type = %q, want image/png", got)
	}
	opened, err := file.Open()
	if err != nil {
		t.Fatalf("file.Open() error = %v", err)
	}
	defer opened.Close()
	data, err := io.ReadAll(opened)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if string(data) != "hello" {
		t.Fatalf("file data = %q, want hello", data)
	}
}

func TestProviderHTTPErrorWarnsAboutUncertainGatewayBilling(t *testing.T) {
	for _, statusCode := range []int{http.StatusRequestTimeout, 499, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout, 524, 599} {
		message := (providerHTTPError{StatusCode: statusCode}).Error()
		if !strings.Contains(message, "可能仍在") || !strings.Contains(message, "产生费用") || !strings.Contains(message, "请勿立即重试") {
			t.Fatalf("providerHTTPError(%d).Error() = %q", statusCode, message)
		}
	}
}

func TestProviderHTTPStatusKeepsExplicitRejectionsRefundable(t *testing.T) {
	for _, statusCode := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusNotImplemented} {
		if ProviderHTTPStatusRequiresBillingReview(statusCode) {
			t.Fatalf("status %d must remain an explicit rejection", statusCode)
		}
	}
}

func TestDoBinaryRejectsOversizedProviderResponse(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxProviderResponseBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	_, _, err := getExternalBinary(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "超过 64MB") {
		t.Fatalf("getExternalBinary() error = %v", err)
	}
}

func TestTextResponseInputIncludesReferenceImages(t *testing.T) {
	input := canvasGenerationInput{
		Prompt: "describe this image",
		Config: providerConfig{SystemPrompt: "answer in Chinese"},
		ReferenceImages: []providerMedia{
			{ID: "image-1", Name: "image.png", Type: "image/png", DataURL: testReferenceImageDataURL},
		},
	}

	value, err := textResponseInput(input)
	if err != nil {
		t.Fatalf("textResponseInput() error = %v", err)
	}
	messages, ok := value.([]map[string]interface{})
	if !ok {
		t.Fatalf("textResponseInput() = %T, want []map[string]interface{}", value)
	}
	if len(messages) != 2 {
		t.Fatalf("len(messages) = %d, want 2", len(messages))
	}
	if messages[0]["role"] != "system" || messages[0]["content"] != "answer in Chinese" {
		t.Fatalf("system message = %#v", messages[0])
	}
	content, ok := messages[1]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("user content = %T, want []map[string]interface{}", messages[1]["content"])
	}
	if len(content) != 2 {
		t.Fatalf("len(content) = %d, want 2", len(content))
	}
	if content[0]["type"] != "input_text" || content[0]["text"] != "describe this image" {
		t.Fatalf("text content = %#v", content[0])
	}
	if content[1]["type"] != "input_image" || content[1]["image_url"] != testReferenceImageDataURL {
		t.Fatalf("image content = %#v", content[1])
	}
}

func TestTextChatContentIncludesReferenceImages(t *testing.T) {
	input := canvasGenerationInput{
		Prompt: "describe this image",
		ReferenceImages: []providerMedia{
			{ID: "image-1", Name: "image.png", Type: "image/png", DataURL: testReferenceImageDataURL},
		},
	}

	value, err := textChatContent(input)
	if err != nil {
		t.Fatalf("textChatContent() error = %v", err)
	}
	content, ok := value.([]map[string]interface{})
	if !ok {
		t.Fatalf("textChatContent() = %T, want []map[string]interface{}", value)
	}
	if len(content) != 2 {
		t.Fatalf("len(content) = %d, want 2", len(content))
	}
	if content[0]["type"] != "text" || content[0]["text"] != "describe this image" {
		t.Fatalf("text content = %#v", content[0])
	}
	imageURL, ok := content[1]["image_url"].(map[string]interface{})
	if !ok {
		t.Fatalf("image_url = %T, want map[string]interface{}", content[1]["image_url"])
	}
	if content[1]["type"] != "image_url" || imageURL["url"] != testReferenceImageDataURL {
		t.Fatalf("image content = %#v", content[1])
	}
}

func TestTextReferenceImageRejectsInternalAssetURL(t *testing.T) {
	_, err := textResponseInput(canvasGenerationInput{
		Prompt: "describe this image",
		ReferenceImages: []providerMedia{
			{ID: "image-1", Name: "image.png", URL: "asset://local-image"},
		},
	})
	if err == nil {
		t.Fatal("textResponseInput() error = nil, want error")
	}
}

func TestRunImageTaskUsesXAIJSONForLegacyGrokImageConfig(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/edits" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", contentType)
		}
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("Decode() error = %v", err)
		}
		image, ok := body["image"].(map[string]interface{})
		if !ok || image["type"] != "image_url" || image["url"] != testReferenceImageDataURL {
			t.Errorf("image = %#v", body["image"])
		}
		if body["response_format"] != "b64_json" {
			t.Errorf("response_format = %#v, want b64_json", body["response_format"])
		}
		for _, unsupported := range []string{"images", "n", "output_format", "quality", "resolution", "size"} {
			if _, exists := body[unsupported]; exists {
				t.Errorf("request body includes unsupported edit field %q: %#v", unsupported, body)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"b64_json":"aGVsbG8=","mime_type":"image/png"}]}`))
	}))
	defer server.Close()

	result, err := runImageTask(context.Background(), canvasGenerationInput{
		Prompt: "turn it into a sketch",
		Config: providerConfig{
			BaseURL:       server.URL + "/v1",
			APIKey:        "test-key",
			Model:         "grok-imagine-image-quality",
			InterfaceType: "openai-image",
			Size:          "auto",
		},
		ReferenceImages: []providerMedia{{ID: "image-1", DataURL: testReferenceImageDataURL}},
	})
	if err != nil {
		t.Fatalf("runImageTask() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("supplier calls = %d, want 1", calls)
	}
	images, ok := result["images"].([]map[string]string)
	if !ok || len(images) != 1 || images[0]["dataUrl"] != testReferenceImageDataURL {
		t.Fatalf("images = %#v", result["images"])
	}
}

func TestXAIImageGenerationUsesOfficialFields(t *testing.T) {
	path, body, err := xaiImageRequest(canvasGenerationInput{
		Prompt: "mountain landscape",
		Config: providerConfig{
			Model:         "grok-imagine-image-quality",
			InterfaceType: "xai-image",
			Size:          "2048x1152",
			Quality:       "auto",
		},
	})
	if err != nil {
		t.Fatalf("xaiImageRequest() error = %v", err)
	}
	if path != "/images/generations" || body["aspect_ratio"] != "16:9" || body["resolution"] != "2k" || body["response_format"] != "b64_json" || body["n"] != 1 {
		t.Fatalf("path = %q, body = %#v", path, body)
	}
	for _, unsupported := range []string{"size", "quality", "output_format", "background"} {
		if _, exists := body[unsupported]; exists {
			t.Fatalf("request body includes unsupported field %q: %#v", unsupported, body)
		}
	}
}

func TestXAIImageDataURLsDownloadsTemporaryResult(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authorization := r.Header.Get("Authorization"); authorization != "" {
			t.Errorf("temporary image Authorization = %q, want empty", authorization)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("image"))
	}))
	defer server.Close()

	images, err := xaiImageDataURLs(context.Background(), imageResponse{Data: []map[string]interface{}{{"url": server.URL + "/temporary.jpeg"}}})
	if err != nil {
		t.Fatalf("xaiImageDataURLs() error = %v", err)
	}
	if len(images) != 1 || images[0]["dataUrl"] != "data:image/jpeg;base64,aW1hZ2U=" {
		t.Fatalf("images = %#v", images)
	}
}

func TestXAIImageMultiEditUsesImagesAndExplicitRatio(t *testing.T) {
	path, body, err := xaiImageRequest(canvasGenerationInput{
		Prompt: "combine them",
		Config: providerConfig{Model: "grok-imagine-image-quality", InterfaceType: "xai-image", Size: "2352x1008"},
		ReferenceImages: []providerMedia{
			{ID: "image-1", DataURL: testReferenceImageDataURL},
			{ID: "image-2", DataURL: "data:image/png;base64,d29ybGQ="},
		},
	})
	if err != nil {
		t.Fatalf("xaiImageRequest() error = %v", err)
	}
	images, ok := body["images"].([]map[string]interface{})
	if path != "/images/edits" || !ok || len(images) != 2 || body["aspect_ratio"] != "20:9" || body["response_format"] != "b64_json" {
		t.Fatalf("path = %q, body = %#v", path, body)
	}
	if _, exists := body["image"]; exists {
		t.Fatalf("multi-image edit includes singular image: %#v", body)
	}
}

func TestXAIImageRejectsUnsupportedInputsBeforeSupplierCall(t *testing.T) {
	tooMany := canvasGenerationInput{
		Config: providerConfig{Model: "grok-imagine-image-quality", InterfaceType: "xai-image"},
		ReferenceImages: []providerMedia{{ID: "1"}, {ID: "2"}, {ID: "3"}, {ID: "4"}},
	}
	if _, _, err := xaiImageRequest(tooMany); err == nil || !strings.Contains(err.Error(), "最多支持 3 张") {
		t.Fatalf("too many references error = %v", err)
	}
	mask := providerMedia{ID: "mask"}
	if _, _, err := xaiImageRequest(canvasGenerationInput{Config: tooMany.Config, Mask: &mask}); err == nil || !strings.Contains(err.Error(), "不支持蒙版") {
		t.Fatalf("mask error = %v", err)
	}
	if _, _, err := xaiImageRequest(canvasGenerationInput{Config: providerConfig{Model: "grok-imagine-image-quality", InterfaceType: "xai-image", TransparentBackground: "true"}}); err == nil || !strings.Contains(err.Error(), "不支持透明背景") {
		t.Fatalf("transparent background error = %v", err)
	}
}

func TestSeedanceVideosBodyUsesVideosEndpointFields(t *testing.T) {
	body, err := seedanceVideosBody(canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{
			Model:              "seedance-2.0-mini-480p",
			Size:               "9:16",
			VideoSeconds:       "8",
			VideoGenerateAudio: "true",
		},
		ReferenceImages: []providerMedia{
			{ID: "image-1", DataURL: testReferenceImageDataURL},
			{ID: "image-2", DataURL: "data:image/png;base64,d29ybGQ="},
		},
		ReferenceVideos: []providerMedia{{ID: "video-1", URL: "https://example.com/ref.mp4"}},
		ReferenceAudios: []providerMedia{{ID: "audio-1", DataURL: "data:audio/mpeg;base64,AAAA"}},
	})
	if err != nil {
		t.Fatalf("seedanceVideosBody() error = %v", err)
	}
	if body["model"] != "seedance-2.0-mini-480p" {
		t.Fatalf("model = %#v", body["model"])
	}
	if body["aspect_ratio"] != "9:16" || body["duration"] != 8 {
		t.Fatalf("size fields = %#v %#v", body["aspect_ratio"], body["duration"])
	}
	if body["generate_audio"] != true {
		t.Fatalf("generate_audio = %#v, want true", body["generate_audio"])
	}
	if body["image_url"] != testReferenceImageDataURL {
		t.Fatalf("image_url = %#v", body["image_url"])
	}
	referenceImages, ok := body["reference_image_urls"].([]string)
	if !ok || len(referenceImages) != 1 || referenceImages[0] != "data:image/png;base64,d29ybGQ=" {
		t.Fatalf("reference_image_urls = %#v", body["reference_image_urls"])
	}
	referenceVideos, ok := body["reference_videos"].([]string)
	if !ok || len(referenceVideos) != 1 || referenceVideos[0] != "https://example.com/ref.mp4" {
		t.Fatalf("reference_videos = %#v", body["reference_videos"])
	}
	referenceAudios, ok := body["reference_audios"].([]string)
	if !ok || len(referenceAudios) != 1 || referenceAudios[0] != "data:audio/mpeg;base64,AAAA" {
		t.Fatalf("reference_audios = %#v", body["reference_audios"])
	}
	if body["content"] != nil || body["ratio"] != nil {
		t.Fatalf("unexpected agent-plan fields in body: %#v", body)
	}
}

func TestSeedanceVideosBodyHonorsGenerateAudio(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "default enabled", want: true},
		{name: "explicit enabled", value: "true", want: true},
		{name: "explicit disabled", value: "false", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, err := seedanceVideosBody(canvasGenerationInput{
				Prompt: "make it move",
				Config: providerConfig{
					Model:              "seedance-2.0-mini-480p",
					VideoGenerateAudio: test.value,
				},
			})
			if err != nil {
				t.Fatalf("seedanceVideosBody() error = %v", err)
			}
			if body["generate_audio"] != test.want {
				t.Fatalf("generate_audio = %#v, want %v", body["generate_audio"], test.want)
			}
		})
	}
}

func TestSeedanceVideosBodyUsesOrderedFrameImageURLsWhenConfigured(t *testing.T) {
	body, err := seedanceVideosBody(canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{Model: "seedance-2.0-mini-480p"},
		ReferenceImages: []providerMedia{
			{ID: "character", DataURL: "data:image/png;base64,Y2hhcmFjdGVy"},
			{ID: "end-frame", DataURL: "data:image/png;base64,d29ybGQ="},
			{ID: "front-frame", DataURL: testReferenceImageDataURL},
		},
		Metadata: map[string]interface{}{"videoStartFrameNodeId": "front-frame", "videoEndFrameNodeId": "end-frame"},
	})
	if err != nil {
		t.Fatalf("seedanceVideosBody() error = %v", err)
	}
	imageURLs, ok := body["image_urls"].([]string)
	if !ok || len(imageURLs) != 3 {
		t.Fatalf("image_urls = %#v", body["image_urls"])
	}
	want := []string{testReferenceImageDataURL, "data:image/png;base64,d29ybGQ=", "data:image/png;base64,Y2hhcmFjdGVy"}
	for index := range want {
		if imageURLs[index] != want[index] {
			t.Fatalf("image_urls = %#v, want %#v", imageURLs, want)
		}
	}
	if body["image_url"] != nil || body["reference_image_urls"] != nil {
		t.Fatalf("unexpected legacy image fields in body: %#v", body)
	}
	if prompt := body["prompt"]; prompt != "make it move" {
		t.Fatalf("prompt = %#v", body["prompt"])
	}
}

func TestRunVideoTaskUsesNewAPIForAnyVideoModel(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("parse create body: %v", err)
			}
			if r.FormValue("model") != "custom-video-v1" || r.FormValue("prompt") != "make it move" {
				t.Errorf("create form = %#v", r.MultipartForm.Value)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video-1","status":"queued"}`))
		case "GET /v1/videos/video-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"video-1","status":"completed"}`))
		case "GET /v1/videos/video-1/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "custom-video-v1"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video, ok := result["video"].(map[string]interface{})
	if !ok || video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", result["video"])
	}
	want := "POST /v1/videos,GET /v1/videos/video-1,GET /v1/videos/video-1/content"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunVideoTaskUsesNestedURLBeforeResultURL(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"task_id":"video-1","status":"queued"}}`))
		case "GET /v1/videos/video-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"task_id":"video-1","status":"SUCCESS","result_url":"` + server.URL + `/v1/videos/video-1/content","data":{"status":"completed","url":"` + server.URL + `/files/video.mp4"}}}`))
		case "GET /files/video.mp4":
			if authorization := r.Header.Get("Authorization"); authorization != "" {
				t.Errorf("file Authorization = %q, want empty", authorization)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		case "GET /v1/videos/video-1/content":
			http.Error(w, "forbidden", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{BaseURL: server.URL, APIKey: "test-key", Model: "custom-video-v1", VideoSeconds: "15"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video, ok := result["video"].(map[string]interface{})
	if !ok || video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", result["video"])
	}
	want := "POST /v1/videos,GET /v1/videos/video-1,GET /files/video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestIsXAIVideoConfigOnlyCorrectsOfficialModelFamily(t *testing.T) {
	for name, tc := range map[string]struct {
		config providerConfig
		want   bool
	}{
		"explicit protocol": {
			config: providerConfig{InterfaceType: "xai-video", Model: "custom-model"},
			want:   true,
		},
		"legacy official model": {
			config: providerConfig{InterfaceType: "newapi", Model: "channel::grok-imagine-video-1.5"},
			want:   true,
		},
		"third party alias": {
			config: providerConfig{InterfaceType: "newapi", Model: "grok-video-1.5"},
			want:   false,
		},
		"other explicit protocol": {
			config: providerConfig{InterfaceType: "newapi-channel-2", Model: "grok-imagine-video-1.5"},
			want:   false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := isXAIVideoConfig(tc.config); got != tc.want {
				t.Fatalf("isXAIVideoConfig() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunVideoTaskCorrectsLegacyGrokVideoConfigToXAIEndpoint(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos/generations":
			if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", contentType)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["model"] != "grok-imagine-video-1.5" || body["prompt"] != "make it move" {
				t.Errorf("request body = %#v", body)
			}
			if body["duration"] != float64(10) || body["aspect_ratio"] != "1:1" || body["resolution"] != "720p" {
				t.Errorf("xAI settings = %#v", body)
			}
			storage, ok := body["storage_options"].(map[string]interface{})
			if !ok || storage["filename"] != "mingwant-video.mp4" || storage["expires_after"] != float64(xaiVideoStoredResultExpiresAfterSeconds) {
				t.Errorf("storage_options = %#v", body["storage_options"])
			}
			for _, legacyField := range []string{"seconds", "size", "images"} {
				if _, exists := body[legacyField]; exists {
					t.Errorf("request body includes legacy field %q: %#v", legacyField, body)
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"request_id":"video-1"}`))
		case "GET /v1/videos/video-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"done","video":{"url":"` + server.URL + `/files/video.mp4"}}`))
		case "GET /files/video.mp4":
			if authorization := r.Header.Get("Authorization"); authorization != "" {
				t.Errorf("file Authorization = %q, want empty", authorization)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{
			BaseURL:       server.URL + "/v1",
			APIKey:        "test-key",
			Model:         "grok-imagine-video-1.5",
			InterfaceType: "newapi",
			VideoSeconds:  "10",
			Size:          "1:1",
			VQuality:      "720",
		},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video, ok := result["video"].(map[string]interface{})
	if !ok || video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", result["video"])
	}
	want := "POST /v1/videos/generations,GET /v1/videos/video-1,GET /files/video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunVideoTaskDownloadsXAIStoredFileBeforeTemporaryURL(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	videoBytes := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'm', 'p', '4', '2'}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos/generations":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"request_id":"video-1"}`))
		case "GET /v1/videos/video-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"done","video":{"url":"` + server.URL + `/temporary/video.mp4","file_output":{"file_id":"file-1"}}}`))
		case "GET /v1/files/file-1/content":
			if authorization := r.Header.Get("Authorization"); authorization != "Bearer test-key" {
				t.Errorf("file Authorization = %q", authorization)
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(videoBytes)
		case "GET /temporary/video.mp4":
			t.Error("temporary vidgen URL should not be requested when file_output is available")
			http.Error(w, "unexpected", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{
			BaseURL:       server.URL + "/v1",
			APIKey:        "test-key",
			Model:         "grok-imagine-video-1.5",
			InterfaceType: "xai-video",
		},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video, ok := result["video"].(map[string]interface{})
	if !ok || video["mimeType"] != "video/mp4" || video["dataUrl"] != "data:video/mp4;base64,AAAAGGZ0eXBtcDQy" {
		t.Fatalf("video = %#v", result["video"])
	}
	want := "POST /v1/videos/generations,GET /v1/videos/video-1,GET /v1/files/file-1/content"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestDownloadXAIVideoResultFallsBackWhenFilesEndpointIsUnavailable(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v1/files/file-1/content":
			http.Error(w, "not supported", http.StatusNotFound)
		case "/temporary/video.mp4":
			if authorization := r.Header.Get("Authorization"); authorization != "" {
				t.Errorf("temporary URL Authorization = %q, want empty", authorization)
			}
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := downloadXAIVideoResult(context.Background(), providerConfig{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-key",
	}, map[string]interface{}{
		"video": map[string]interface{}{
			"url":         server.URL + "/temporary/video.mp4",
			"file_output": map[string]interface{}{"file_id": "file-1"},
		},
	})
	if err != nil {
		t.Fatalf("downloadXAIVideoResult() error = %v", err)
	}
	video, ok := result["video"].(map[string]interface{})
	if !ok || video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", result["video"])
	}
	want := "GET /v1/files/file-1/content,GET /temporary/video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestXAIVideoBodyUsesOfficialImageShapeAndNormalizesSettings(t *testing.T) {
	body, err := xaiVideoBody(canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{
			Model:         "grok-imagine-video-1.5",
			InterfaceType: "xai-video",
			VideoSeconds:  "20",
			Size:          "1024x1792",
			VQuality:      "1080",
		},
		ReferenceImages: []providerMedia{{ID: "image-1", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err != nil {
		t.Fatalf("xaiVideoBody() error = %v", err)
	}
	if body["duration"] != 15 || body["aspect_ratio"] != "9:16" || body["resolution"] != "1080p" {
		t.Fatalf("xAI settings = %#v", body)
	}
	storage, ok := body["storage_options"].(map[string]interface{})
	if !ok || storage["filename"] != "mingwant-video.mp4" || storage["expires_after"] != xaiVideoStoredResultExpiresAfterSeconds {
		t.Fatalf("storage_options = %#v", body["storage_options"])
	}
	image, ok := body["image"].(map[string]interface{})
	if !ok || image["url"] != testReferenceImageDataURL {
		t.Fatalf("image = %#v", body["image"])
	}
	for _, legacyField := range []string{"seconds", "size", "images"} {
		if _, exists := body[legacyField]; exists {
			t.Fatalf("body includes legacy field %q: %#v", legacyField, body)
		}
	}
}

func TestXAIVideoBodyRejectsMultipleStartImages(t *testing.T) {
	_, err := xaiVideoBody(canvasGenerationInput{
		Config: providerConfig{Model: "grok-imagine-video-1.5", InterfaceType: "xai-video"},
		ReferenceImages: []providerMedia{
			{ID: "image-1", DataURL: testReferenceImageDataURL},
			{ID: "image-2", DataURL: testReferenceImageDataURL},
		},
		Metadata: map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err == nil || !strings.Contains(err.Error(), "只支持 1 张起始图") {
		t.Fatalf("xaiVideoBody() error = %v", err)
	}
}

func TestXAIVideoBodyKeepsSourceRatioForAutomaticImageToVideo(t *testing.T) {
	body, err := xaiVideoBody(canvasGenerationInput{
		Prompt:          "make it move",
		Config:          providerConfig{Model: "grok-imagine-video-1.5", InterfaceType: "xai-video", Size: "auto"},
		ReferenceImages: []providerMedia{{ID: "image-1", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err != nil {
		t.Fatalf("xaiVideoBody() error = %v", err)
	}
	if _, exists := body["aspect_ratio"]; exists {
		t.Fatalf("automatic image-to-video body includes aspect_ratio: %#v", body)
	}
}

func TestXAIVideoBodyRejectsImageToVideoWithoutStartImage(t *testing.T) {
	_, err := xaiVideoBody(canvasGenerationInput{
		Config:   providerConfig{Model: "grok-imagine-video-1.5", InterfaceType: "xai-video"},
		Metadata: map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err == nil || !strings.Contains(err.Error(), "必须提供 1 张起始图") {
		t.Fatalf("xaiVideoBody() error = %v", err)
	}
}

func TestRunVideoTaskTreatsXAIExpiredStatusAsTerminal(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos/generations":
			_, _ = w.Write([]byte(`{"request_id":"video-1"}`))
		case "GET /v1/videos/video-1":
			_, _ = w.Write([]byte(`{"status":"expired"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	_, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{
			BaseURL:       server.URL + "/v1",
			APIKey:        "test-key",
			Model:         "grok-imagine-video-1.5",
			InterfaceType: "xai-video",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "视频生成失败") {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	want := "POST /v1/videos/generations,GET /v1/videos/video-1"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestNewAPIVideoPromptKeepsTextOnlyPromptUnchanged(t *testing.T) {
	input := canvasGenerationInput{
		Prompt: "make it move",
	}
	if prompt := newAPIVideoPromptText(input); prompt != "make it move" {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestVideoProviderPromptsKeepReferencePromptUnchanged(t *testing.T) {
	input := canvasGenerationInput{
		Prompt:          "镜头缓慢前推，人物走向门口",
		ReferenceImages: []providerMedia{{ID: "image-1", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoEditOperation": "image_to_video"},
	}
	for name, prompt := range map[string]string{
		"newapi":           newAPIVideoPromptText(input),
		"seedance-content": seedancePromptText(input),
		"seedance-videos":  seedanceVideosPromptText(input),
	} {
		if prompt != input.Prompt {
			t.Fatalf("%s prompt = %q", name, prompt)
		}
	}
}

func TestNewAPIVideoOmitsImagesForTextToVideoOperation(t *testing.T) {
	input := canvasGenerationInput{
		Prompt: "make it move with the described character",
		ReferenceImages: []providerMedia{
			{ID: "image-1", DataURL: testReferenceImageDataURL},
		},
		Metadata: map[string]interface{}{"videoEditOperation": "text_to_video"},
	}
	if shouldSendNewAPIVideoImages(input) {
		t.Fatal("shouldSendNewAPIVideoImages() = true, want false")
	}
	if prompt := newAPIVideoPromptText(input); strings.Contains(prompt, "@image1") {
		t.Fatalf("prompt = %q", prompt)
	}
}

func TestSeedanceVideosBodyRequiresImageForVideoOrAudioReferences(t *testing.T) {
	_, err := seedanceVideosBody(canvasGenerationInput{
		Prompt:          "make it move",
		Config:          providerConfig{Model: "seedance-2.0-mini-480p"},
		ReferenceVideos: []providerMedia{{ID: "video-1", URL: "https://example.com/ref.mp4"}},
	})
	if err == nil {
		t.Fatal("seedanceVideosBody() error = nil, want error")
	}
}

func TestArkPlanConfigStaysSeparateFromSeedanceVideosEndpoint(t *testing.T) {
	config := providerConfig{BaseURL: "https://ark.cn-beijing.volces.com/api/plan/v3", Model: "seedance-2.0-pro"}
	if !isArkPlanVideoConfig(config) {
		t.Fatal("isArkPlanVideoConfig() = false, want true")
	}
	if !isSeedanceVideoConfig(config) {
		t.Fatal("isSeedanceVideoConfig() = false, want true")
	}
}

func TestNewAPIChannel1VideoBodyMapsFramesAndReferences(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()

	body, err := newAPIChannel1VideoBody(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{Model: "seedance-2.0", Size: "9:16", VQuality: "1080", VideoSeconds: "15", VideoWatermark: "true"},
		ReferenceImages: []providerMedia{
			{ID: "first", URL: server.URL + "/first.png"},
			{ID: "last", URL: server.URL + "/last.png"},
			{ID: "character", URL: server.URL + "/character.png"},
		},
		ReferenceVideos: []providerMedia{{ID: "video", URL: server.URL + "/reference.mp4"}},
		ReferenceAudios: []providerMedia{{ID: "voice", URL: server.URL + "/voice.mp3"}},
		Metadata:        map[string]interface{}{"videoStartFrameNodeId": "first", "videoEndFrameNodeId": "last"},
	})
	if err != nil {
		t.Fatalf("newAPIChannel1VideoBody() error = %v", err)
	}
	input := body["input"].(map[string]interface{})
	media := input["media"].([]map[string]string)
	wantTypes := []string{"first_frame", "last_frame", "reference_image", "reference_video", "reference_voice"}
	if len(media) != len(wantTypes) {
		t.Fatalf("media = %#v", media)
	}
	for index, want := range wantTypes {
		if media[index]["type"] != want {
			t.Fatalf("media[%d].type = %q, want %q", index, media[index]["type"], want)
		}
	}
	parameters := body["parameters"].(map[string]interface{})
	if parameters["resolution"] != "1080P" || parameters["ratio"] != "9:16" || parameters["duration"] != 15 || parameters["watermark"] != true {
		t.Fatalf("parameters = %#v", parameters)
	}
}

func TestNewAPIChannel1VideoBodyRejectsInlineMedia(t *testing.T) {
	_, err := newAPIChannel1VideoBody(context.Background(), canvasGenerationInput{
		Prompt:          "make it move",
		Config:          providerConfig{Model: "seedance-2.0"},
		ReferenceImages: []providerMedia{{ID: "image", DataURL: testReferenceImageDataURL}},
	})
	if err == nil || !strings.Contains(err.Error(), "公网 HTTP(S) URL") {
		t.Fatalf("newAPIChannel1VideoBody() error = %v", err)
	}
}

func TestRunNewAPIChannel1VideoTaskDownloadsSucceededObject(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/videos":
			if contentType := r.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
				t.Errorf("Content-Type = %q", contentType)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["model"] != "seedance-2.0" {
				t.Errorf("body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"channel-1-task","task_id":"channel-1-task","status":"RUNNING"}`))
		case "GET /v1/videos/channel-1-task":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"channel-1-task","status":"SUCCEEDED","object":"` + server.URL + `/video.mp4"}`))
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runNewAPIChannel1VideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "seedance-2.0", InterfaceType: "newapi-channel-1"},
	})
	if err != nil {
		t.Fatalf("runNewAPIChannel1VideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", video)
	}
	want := "POST /v1/videos,GET /v1/videos/channel-1-task,GET /video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunNewAPIChannel2VideoTaskDownloadsTemporaryResult(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/video/generations":
			if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
				t.Errorf("Authorization = %q", auth)
			}
			var body map[string]interface{}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["model"] != "grok-image-video" || body["seconds"] != "10" || body["aspect_ratio"] != "9:16" || body["resolution"] != "720p" {
				t.Errorf("body = %#v", body)
			}
			images, ok := body["image_urls"].([]interface{})
			if !ok || len(images) != 2 || images[0] != testReferenceImageDataURL {
				t.Errorf("image_urls = %#v", body["image_urls"])
			}
			videos, _ := body["video_urls"].([]interface{})
			audios, _ := body["audio_urls"].([]interface{})
			if len(videos) != 1 || len(audios) != 1 || body["generate_audio"] != true {
				t.Errorf("multi-reference body = %#v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"task_id":"grok-task","status":"queued"}`))
		case "GET /v1/video/generations/grok-task":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":"success","data":{"task_id":"grok-task","status":"SUCCESS","result_url":"` + server.URL + `/video.mp4"}}`))
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{BaseURL: server.URL, APIKey: "test-key", Model: "grok-image-video", InterfaceType: "newapi-channel-2", VideoSeconds: "15", Size: "720x1280", VQuality: "high"},
		ReferenceImages: []providerMedia{
			{ID: "image-1", DataURL: testReferenceImageDataURL},
			{ID: "image-2", DataURL: testReferenceImageDataURL},
		},
		ReferenceVideos: []providerMedia{{ID: "video-1", URL: server.URL + "/reference.mp4"}},
		ReferenceAudios: []providerMedia{{ID: "audio-1", URL: server.URL + "/reference.mp3"}},
		Metadata:        map[string]interface{}{"videoEditOperation": "image_to_video"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", video)
	}
	want := "POST /v1/video/generations,GET /v1/video/generations/grok-task,GET /video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestRunGeminiVeoVideoTaskUsesLongRunningOperation(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	paths := make([]string, 0, 3)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		if r.Header.Get("x-goog-api-key") != "test-key" {
			t.Errorf("x-goog-api-key = %q", r.Header.Get("x-goog-api-key"))
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /v1beta/models/veo-test:predictLongRunning":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"operations/op-1"}`))
		case "GET /v1beta/operations/op-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"done":true,"response":{"generatedSamples":[{"video":{"uri":"` + server.URL + `/video.mp4"}}]}}`))
		case "GET /video.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("video"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := runVideoTask(context.Background(), canvasGenerationInput{
		Prompt: "make it move",
		Config: providerConfig{BaseURL: server.URL, APIKey: "test-key", APIFormat: "gemini", Model: "veo-test", InterfaceType: "gemini-veo", VideoSeconds: "6", Size: "16:9", VQuality: "720"},
	})
	if err != nil {
		t.Fatalf("runVideoTask() error = %v", err)
	}
	video := result["video"].(map[string]interface{})
	if video["dataUrl"] != "data:video/mp4;base64,dmlkZW8=" {
		t.Fatalf("video = %#v", video)
	}
	want := "POST /v1beta/models/veo-test:predictLongRunning,GET /v1beta/operations/op-1,GET /video.mp4"
	if got := strings.Join(paths, ","); got != want {
		t.Fatalf("paths = %q, want %q", got, want)
	}
}

func TestEnrichAPICallLogReadsGeminiVeoOperationName(t *testing.T) {
	log := model.ApiCallLog{APIFormat: "gemini", Capability: "video", RequestKind: "create"}
	(&Service{}).EnrichAPICallLog(&log, []byte(`{"name":"operations/op-1"}`))
	if log.ProviderRequestID != "operations/op-1" {
		t.Fatalf("ProviderRequestID = %q", log.ProviderRequestID)
	}
}

func TestNewAPIChannel2SingleImageModelsRequireOneReference(t *testing.T) {
	_, err := newAPIChannel2VideoBody(canvasGenerationInput{Config: providerConfig{Model: "grok-video-1.5", VideoSeconds: "6"}})
	if err == nil {
		t.Fatal("newAPIChannel2VideoBody() error = nil")
	}
	if !strings.Contains(err.Error(), "当前 0 张") {
		t.Fatalf("newAPIChannel2VideoBody() error = %q", err)
	}
}

func TestNewAPIChannel2SingleImageModelUsesReferenceForStaleTextToVideoMetadata(t *testing.T) {
	body, err := newAPIChannel2VideoBody(canvasGenerationInput{
		Config:          providerConfig{Model: "grok-video-1.5", VideoSeconds: "6"},
		ReferenceImages: []providerMedia{{ID: "image-1", DataURL: testReferenceImageDataURL}},
		Metadata:        map[string]interface{}{"videoEditOperation": "text_to_video"},
	})
	if err != nil {
		t.Fatalf("newAPIChannel2VideoBody() error = %v", err)
	}
	images, ok := body["image_urls"].([]string)
	if !ok || len(images) != 1 || images[0] != testReferenceImageDataURL {
		t.Fatalf("image_urls = %#v", body["image_urls"])
	}
}

func TestValidateGenerationInterfaceRejectsMismatchedType(t *testing.T) {
	if err := validateGenerationInterface("video", "chat-completion"); err == nil {
		t.Fatal("validateGenerationInterface() error = nil")
	}
	if err := validateGenerationInterface("video", "newapi-channel-1"); err != nil {
		t.Fatalf("validateGenerationInterface() error = %v", err)
	}
	if err := validateGenerationInterface("video", "newapi-channel-2"); err != nil {
		t.Fatalf("validateGenerationInterface() error = %v", err)
	}
	if err := validateGenerationInterface("video", "xai-video"); err != nil {
		t.Fatalf("validateGenerationInterface() error = %v", err)
	}
	if err := validateGenerationInterface("image", "xai-image"); err != nil {
		t.Fatalf("validateGenerationInterface() error = %v", err)
	}
}

func TestProcessTaskValidatesInterfaceBeforeHydratingMedia(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	input := canvasGenerationInput{
		Mode:            "video",
		Prompt:          "make it move",
		Config:          providerConfig{BaseURL: server.URL + "/v1", APIKey: "key", Model: "text-model", InterfaceType: "chat-completion"},
		ReferenceImages: []providerMedia{{StorageKey: "resource:missing"}},
	}
	raw, _ := json.Marshal(input)
	_, err := (&Service{}).processCanvasGenerationTask(context.Background(), "user-1", "video_generate", "", string(raw))
	if err == nil || !strings.Contains(err.Error(), "不支持video生成") {
		t.Fatalf("processCanvasGenerationTask() error = %v", err)
	}
}
