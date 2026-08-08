package service

import (
	"strings"
	"testing"
)

func TestStoryboardCinematicQualityContractIncludesRequestedCountAndDuration(t *testing.T) {
	contract := storyboardCinematicQualityContract(30, 7)
	if !strings.Contains(contract, "严格等于 30 秒") {
		t.Fatalf("contract does not include requested duration: %s", contract)
	}
	if !strings.Contains(contract, "严格输出 7 个镜头") {
		t.Fatalf("contract does not include requested shot count: %s", contract)
	}
}

func TestStoryboardContractUsesMediaNeutralCameraGuide(t *testing.T) {
	contract := storyboardCinematicQualityContract(0, 0)
	for _, term := range []string{"真人、二维、三维、定格、绘本或混合媒介", "固定视点与运动视点都有效", "动画或其他媒介", "没有通用配额", "endBoundary", "单个完整 JSON 对象"} {
		if !strings.Contains(contract, term) {
			t.Fatalf("camera language guide is missing %q: %s", term, contract)
		}
	}
	for _, forbidden := range []string{"优先使用真实电影机语言", "3D动漫、动画、二次元", "ECU 每场景最多", "通常保持 5-8 秒以上", "全片各不超过 1-2 次"} {
		if strings.Contains(contract+defaultStoryboardPromptTemplate(), forbidden) {
			t.Fatalf("storyboard prompt still contains universal medium rule %q", forbidden)
		}
	}
}

func TestStoryboardPromptsLeaveAspectRatioToVideoNode(t *testing.T) {
	contract := storyboardCinematicQualityContract(0, 0)
	if !strings.Contains(contract, "不要讨论画幅配置") {
		t.Fatalf("contract does not delegate aspect ratio: %s", contract)
	}
	if strings.Contains(defaultStoryboardPromptTemplate(), "2.39:1") {
		t.Fatal("default storyboard prompt still hard-codes 2.39:1")
	}
	plan := fallbackAgentStoryboardPlan("测试故事")
	if strings.Contains(plan.StyleGuide+plan.Shots[0].VideoPrompt, "2.39:1") || strings.Contains(plan.StyleGuide+plan.Shots[0].VideoPrompt, "画幅") {
		t.Fatal("fallback storyboard still mentions output aspect ratio")
	}
}

func TestValidateStoryboardShotCount(t *testing.T) {
	plan := agentStoryboardPlan{Shots: make([]agentStoryboardShot, 3)}
	if err := validateStoryboardShotCount(plan, 3); err != nil {
		t.Fatalf("expected matching shot count to pass: %v", err)
	}
	if err := validateStoryboardShotCount(plan, 2); err == nil {
		t.Fatal("expected mismatched shot count to fail")
	}
	if err := validateStoryboardShotCount(plan, 0); err != nil {
		t.Fatalf("expected automatic shot count to pass: %v", err)
	}
}

func TestStoryboardImageAndVideoPromptsKeepBoundaryResponsibilitiesSeparate(t *testing.T) {
	shot := agentStoryboardShot{
		VisualPrompt:  "冻结画面内容",
		VideoPrompt:   "执行有序变化",
		ShotSize:      "中景",
		Camera:        "固定视点",
		Motion:        "主体画内移动",
		TimeBeats:     "0-4秒：完成变化",
		StartBoundary: &projectShotBoundary{Positions: []string{"开始位置"}},
		EndBoundary:   &projectShotBoundary{Positions: []string{"结束位置"}},
	}
	imagePrompt := buildStoryboardImagePrompt("二维手绘", shot)
	if !strings.Contains(imagePrompt, "开始位置") || strings.Contains(imagePrompt, "结束位置") || strings.Contains(imagePrompt, "执行有序变化") {
		t.Fatalf("image prompt crossed boundary responsibilities: %s", imagePrompt)
	}
	videoPrompt := buildStoryboardVideoPrompt("二维手绘", shot)
	if !strings.Contains(videoPrompt, "开始位置") || !strings.Contains(videoPrompt, "结束位置") || strings.Contains(videoPrompt, "冻结画面内容") {
		t.Fatalf("video prompt crossed boundary responsibilities: %s", videoPrompt)
	}
}
