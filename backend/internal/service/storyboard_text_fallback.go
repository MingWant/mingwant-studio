package service

import (
	"regexp"
	"strconv"
	"strings"
)

// 某些兼容网关会完整返回带“镜头/画面/提示词”标签的文本，却忽略 JSON MIME 要求。
// 只在没有 JSON 容器、且能识别出镜头边界时收敛这种完整文本；截断 JSON 仍然必须失败并进入费用核对边界。
var storyboardTextShotMarker = regexp.MustCompile(`(?im)(?:^|\n)\s*(?:(?:镜头|分镜|shot)\s*\d{0,2}|\d{1,2}[、.)])\s*[:：.)、-]?\s*`)
var storyboardTextDuration = regexp.MustCompile(`\d{1,3}`)

func parseStoryboardTextPlan(raw string, defaultDuration int) (agentStoryboardPlan, bool) {
	text := strings.TrimSpace(strings.TrimPrefix(raw, "\ufeff"))
	if text == "" || strings.ContainsAny(text, "{}[]") {
		return agentStoryboardPlan{}, false
	}
	matches := storyboardTextShotMarker.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return agentStoryboardPlan{}, false
	}
	plan := agentStoryboardPlan{Title: "影视分镜", Logline: "根据剧情生成的分镜方案"}
	for index, match := range matches {
		end := len(text)
		if index+1 < len(matches) {
			end = matches[index+1][0]
		}
		if shot, ok := parseStoryboardTextShot(text[match[1]:end]); ok {
			plan.Shots = append(plan.Shots, shot)
		}
	}
	if len(plan.Shots) == 0 || normalizeAndValidateStoryboardPlanWithDefaults(&plan, defaultDuration) != nil {
		return agentStoryboardPlan{}, false
	}
	return plan, true
}

func parseStoryboardTextShot(block string) (agentStoryboardShot, bool) {
	lines := strings.Split(block, "\n")
	description := storyboardTextField(lines, "画面描述", "镜头画面", "画面", "场景", "内容", "scene", "visual")
	imagePrompt := storyboardTextField(lines, "首帧图片提示词", "首帧提示词", "分镜图提示词", "图片生成提示词", "图片提示词", "画面提示词", "image prompt", "image_prompt")
	videoPrompt := storyboardTextField(lines, "视频动作提示词", "视频动作", "视频生成提示词", "视频提示词", "动作提示词", "运镜", "video prompt", "video_prompt")
	if description == "" {
		for _, line := range lines {
			candidate := strings.TrimSpace(strings.TrimLeft(line, "-*•"))
			if candidate == "" || storyboardTextIsLabeledLine(candidate) {
				continue
			}
			description = candidate
			break
		}
	}
	if description == "" {
		description = firstStoryboardText(imagePrompt, videoPrompt)
	}
	if description == "" && imagePrompt == "" && videoPrompt == "" {
		return agentStoryboardShot{}, false
	}

	shot := agentStoryboardShot{
		Description:   description,
		VisualPrompt:  imagePrompt,
		VideoPrompt:   videoPrompt,
		Duration:      storyboardTextDurationValue(storyboardTextField(lines, "时长", "duration", "seconds", "秒数")),
		Dialogue:      storyboardTextField(lines, "台词", "对白", "dialogue"),
		ShotSize:      storyboardTextField(lines, "景别", "shot size", "shot_size"),
		Lighting:      storyboardTextField(lines, "灯光", "光线", "氛围", "lighting", "atmosphere"),
		Camera:        storyboardTextField(lines, "镜头", "机位", "camera"),
		Motion:        storyboardTextField(lines, "镜头运动", "运动", "motion"),
		Negative:      storyboardTextField(lines, "负面提示词", "负面", "negative prompt", "negative_prompt"),
	}
	return shot, true
}

func storyboardTextField(lines []string, labels ...string) string {
	for _, rawLine := range lines {
		line := strings.TrimSpace(strings.TrimLeft(rawLine, "-*•"))
		lower := strings.ToLower(line)
		for _, label := range labels {
			prefix := strings.ToLower(label)
			if !strings.HasPrefix(lower, prefix) {
				continue
			}
			rest := strings.TrimSpace(line[len(label):])
			if rest == "" {
				continue
			}
			if strings.HasPrefix(rest, ":") || strings.HasPrefix(rest, "：") || strings.HasPrefix(rest, "-") {
				return strings.TrimSpace(rest[1:])
			}
		}
	}
	return ""
}

func storyboardTextIsLabeledLine(line string) bool {
	return storyboardTextField([]string{line}, "画面描述", "镜头画面", "画面", "场景", "内容", "首帧提示词", "分镜图提示词", "图片生成提示词", "图片提示词", "画面提示词", "视频动作提示词", "视频动作", "视频生成提示词", "视频提示词", "动作提示词", "运镜", "时长", "duration", "seconds", "秒数", "台词", "对白", "dialogue", "景别", "shot size", "镜头", "机位", "灯光", "光线", "氛围", "lighting", "镜头运动", "运动", "motion", "负面提示词", "负面") != ""
}

func storyboardTextDurationValue(value string) int {
	match := storyboardTextDuration.FindString(strings.TrimSpace(value))
	if match == "" {
		return 0
	}
	result, _ := strconv.Atoi(match)
	return result
}

func firstStoryboardText(values ...string) string {
	for _, value := range values {
		if text := strings.TrimSpace(value); text != "" {
			return text
		}
	}
	return ""
}
