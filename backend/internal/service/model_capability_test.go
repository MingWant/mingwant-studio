package service

import (
	"strings"
	"testing"

	"infinite-canvas/backend/internal/model"
)

func TestDefaultModelCapabilityConfigUsesProtocolLimits(t *testing.T) {
	veo := DefaultModelCapabilityConfig(string(model.ChannelInterfaceGeminiVeo)).Video
	if veo == nil || veo.Duration.Selection != "enum" || !containsInt(veo.Duration.Values, 8) || veo.References.MaxImages != 1 {
		t.Fatalf("Gemini Veo default capability = %#v", veo)
	}
	media := DefaultModelCapabilityConfig(string(model.ChannelInterfaceNewAPIChannel1)).Video
	if media == nil || media.References.MaxVideos != 3 || media.References.MaxAudios != 3 || !media.GenerateAudio.Supported {
		t.Fatalf("NewAPI media default capability = %#v", media)
	}
}

func TestValidateVideoTaskRejectsCapabilityViolations(t *testing.T) {
	profile := DefaultModelCapabilityConfig(string(model.ChannelInterfaceNewAPIChannel1)).Video
	base := canvasGenerationInput{
		Mode:   "video",
		Prompt: "一段城市夜景",
		Config: providerConfig{InterfaceType: string(model.ChannelInterfaceNewAPIChannel1), VideoSeconds: "6", Size: "1280x720", VQuality: "720", VideoGenerateAudio: "true"},
	}
	if err := validateVideoTask(profile, base); err != nil {
		t.Fatalf("valid video task rejected: %v", err)
	}
	tooLong := base
	tooLong.Prompt = strings.Repeat("字", profile.References.PromptMaxChars+1)
	if err := validateVideoTask(profile, tooLong); err == nil {
		t.Fatal("overlong video prompt was accepted")
	}
	tooLarge := base
	tooLarge.ReferenceVideos = []providerMedia{{Bytes: profile.References.MaxVideoBytes + 1, DurationMs: 1_000}}
	if err := validateVideoTask(profile, tooLarge); err == nil {
		t.Fatal("oversized reference video was accepted")
	}
	unsupportedOperation := base
	unsupportedOperation.Metadata = map[string]interface{}{"videoEditOperation": "inpaint"}
	if err := validateVideoTask(profile, unsupportedOperation); err == nil {
		t.Fatal("unsupported video operation was accepted")
	}
}

func TestVideoCapabilityNormalizesPixelRatioAndResolution(t *testing.T) {
	profile := DefaultModelCapabilityConfig(string(model.ChannelInterfaceXAIVideo)).Video
	input := canvasGenerationInput{Mode: "video", Prompt: "test", Config: providerConfig{InterfaceType: string(model.ChannelInterfaceXAIVideo), VideoSeconds: "6", Size: "1920x1080", VQuality: "1080"}}
	if err := validateVideoTask(profile, input); err != nil {
		t.Fatalf("pixel ratio should match configured aspect ratio: %v", err)
	}
	input.Config.Size = "2:1"
	if err := validateVideoTask(profile, input); err == nil {
		t.Fatal("unsupported aspect ratio was accepted")
	}
}

func TestNormalizeModelCapabilityConfigCanonicalizesAliases(t *testing.T) {
	input := DefaultModelCapabilityConfig(string(model.ChannelInterfaceXAIVideo))
	input.Version = 99
	input.Video.Ratios = []string{"16x9", "AUTO"}
	input.Video.DefaultRatio = "auto"
	input.Video.Resolutions = []string{"4k", "720"}
	input.Video.DefaultResolution = "4K"
	input.Video.Operations = []string{"TEXT_TO_VIDEO"}
	input.Video.DefaultOperation = "text_to_video"

	normalized, err := NormalizeModelCapabilityConfig("video", string(model.ChannelInterfaceXAIVideo), input)
	if err != nil {
		t.Fatalf("NormalizeModelCapabilityConfig() error = %v", err)
	}
	if normalized.Video.DefaultRatio != "adaptive" || normalized.Video.Ratios[0] != "16:9" || normalized.Video.Ratios[1] != "adaptive" {
		t.Fatalf("normalized ratios = %#v", normalized.Video.Ratios)
	}
	if normalized.Video.DefaultResolution != "2160p" || normalized.Video.Resolutions[0] != "2160p" || normalized.Video.Resolutions[1] != "720p" {
		t.Fatalf("normalized resolutions = %#v", normalized.Video.Resolutions)
	}
}
