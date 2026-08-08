package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
)

func TestParseAgentStoryboardPlanAcceptsProseAndFencedJSON(t *testing.T) {
	raw := "下面是结果：\n```json\n" + validStoryboardResponseJSON() + "\n```\n请查收。"
	plan, err := parseAgentStoryboardPlan(raw)
	if err != nil {
		t.Fatalf("expected fenced JSON to parse: %v", err)
	}
	if len(plan.Shots) != 1 || plan.Shots[0].Duration != 8 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
}

func TestParseAgentStoryboardPlanAcceptsTopLevelArrayAndCommonAliases(t *testing.T) {
	raw := `[{
        "name":"开场",
        "plotDescription":"主角抬头",
        "shotPurpose":"建立人物",
        "information_change":"未知人物 -> 看清主角",
        "start_boundary":"主角坐在桌边",
        "end_boundary":{"position":"主角抬头看向门口"},
        "duration":"8秒",
        "imagePrompt":"主角坐在桌边的冻结画面",
        "motionPrompt":"主角从低头到抬头看门口",
        "camera":"固定中景",
        "motion":"主体画内动作",
        "timing":"0-4秒低头；4-8秒抬头",
        "tags":"角色:主角"
    }]`
	plan, err := parseAgentStoryboardPlan(raw)
	if err != nil {
		t.Fatalf("expected aliased shot array to parse: %v", err)
	}
	shot := plan.Shots[0]
	if shot.Duration != 8 || shot.Description != "主角抬头" || len(shot.AssetTags) != 1 {
		t.Fatalf("aliases were not normalized: %#v", shot)
	}
}

func TestParseAgentStoryboardPlanNormalizesObjectCharacterLists(t *testing.T) {
	raw := strings.Replace(validStoryboardResponseJSON(), `"characters":["主角"]`, `"characters":[{"name":"主角","role":"男主"}]`, 1)
	plan, err := parseAgentStoryboardPlan(raw)
	if err != nil {
		t.Fatalf("expected object character list to parse: %v", err)
	}
	if len(plan.Characters) != 1 || !strings.Contains(plan.Characters[0], "主角") {
		t.Fatalf("character list was not normalized: %#v", plan.Characters)
	}
}

func TestParseAgentStoryboardPlanRejectsShotsFromTruncatedOuterObject(t *testing.T) {
	raw := strings.TrimSuffix(validStoryboardResponseJSON(), "}")
	if _, err := parseAgentStoryboardPlan(raw); err == nil {
		t.Fatal("expected truncated outer object to require the repair path")
	}
}

func TestStoryboardRepairPromptKeepsOriginalTask(t *testing.T) {
	prompt := buildStoryboardRepairPrompt("原始剧情任务", "我不能输出 JSON", errors.New("不是 JSON"))
	for _, expected := range []string{"原始剧情任务", "我不能输出 JSON", "根据 original_task 重新生成", "第一个字符必须是 {"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("repair prompt is missing %q: %s", expected, prompt)
		}
	}
}

func TestStoryboardRepairDoesNotSendSecondRequestWhenWorkerLeaseExpired(t *testing.T) {
	t.Setenv("CANVAS_ALLOW_PRIVATE_UPSTREAMS", "true")
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"不是 JSON\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()

	svc, db := newWorkerSafetyTestService(t)
	expired := time.Now().Add(-time.Second)
	task := model.Task{ID: "task-storyboard-expired", UserID: "user-1", Type: "agent_storyboard", Status: model.TaskStatusRunning, LeaseOwner: "worker-old", LeaseExpiresAt: &expired, ProviderCallState: model.TaskProviderCallPrepared}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	config := providerConfig{BaseURL: server.URL + "/v1", APIKey: "test-key", Model: "slow-storyboard", InterfaceType: "chat-completion"}
	_, err := svc.requestAgentStoryboardPlan(context.Background(), task, "生成一镜分镜", config, 8, 1, true)
	if err == nil || !strings.Contains(err.Error(), "没有发送第二次模型请求") {
		t.Fatalf("requestAgentStoryboardPlan() error = %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want exactly one initial request", calls.Load())
	}
}

func TestBuildAgentStoryboardResultIncludesStructuredScriptNode(t *testing.T) {
	task := model.Task{ID: "task-agent-structured", Prompt: "生成一段短剧"}
	plan := fallbackAgentStoryboardPlan(task.Prompt)
	_, ops, err := buildAgentStoryboardResult(task, plan, nil)
	if err != nil {
		t.Fatalf("buildAgentStoryboardResult() error = %v", err)
	}
	var storyboard map[string]any
	for _, op := range ops {
		if op["type"] != "add_node" || op["nodeType"] != "script" {
			continue
		}
		metadata, _ := op["metadata"].(map[string]any)
		if metadata["workflowKind"] == "storyboard" {
			storyboard = metadata
			break
		}
	}
	if storyboard == nil {
		t.Fatal("影视 Agent 结果没有结构化分镜脚本节点")
	}
	data, ok := storyboard["storyboard"].(map[string]any)
	if !ok {
		t.Fatalf("storyboard metadata = %#v", storyboard["storyboard"])
	}
	rows, ok := data["rows"].([]map[string]any)
	if !ok || len(rows) != len(plan.Shots) {
		t.Fatalf("storyboard rows = %#v, want %d rows", data["rows"], len(plan.Shots))
	}
	if rows[0]["videoMotionPrompt"] == "" || rows[0]["imageGenerationPrompt"] == "" {
		t.Fatalf("storyboard prompts = %#v", rows[0])
	}
}

func TestProcessAgentStoryboardTaskRequiresProviderConfig(t *testing.T) {
	svc, _ := newWorkerSafetyTestService(t)
	_, _, err := svc.processAgentStoryboardTask(context.Background(), model.Task{ID: "task-agent-no-config", Prompt: "生成短剧分镜"})
	if err == nil || !strings.Contains(err.Error(), "请先配置可用的文本模型") {
		t.Fatalf("missing provider config error = %v", err)
	}
}

func validStoryboardResponseJSON() string {
	return `{
        "title":"测试分镜",
        "logline":"一句话剧情",
        "styleGuide":"二维手绘",
        "characters":["主角"],
        "locations":["房间"],
        "shots":[{
            "title":"开场",
            "description":"主角抬头",
            "purpose":"建立人物",
            "informationChange":"未知人物 -> 看清主角",
            "startBoundary":{"positions":["主角坐在桌边"]},
            "endBoundary":{"positions":["主角抬头看向门口"]},
            "durationSeconds":8,
            "dialogue":"",
            "shotSize":"中景",
            "emotion":"警觉",
            "lightingAndAtmosphere":"室内暖光",
            "audioEffects":"敲门声",
            "visualPrompt":"主角坐在桌边的冻结画面",
            "videoPrompt":"主角从低头到抬头看门口",
            "camera":"固定中景",
            "motion":"主体画内动作",
            "timeBeats":"0-4秒低头；4-8秒抬头",
            "negativePrompt":"",
            "assetTags":[]
        }]
    }`
}
