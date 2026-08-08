package service

import (
	"testing"
	"time"

	"infinite-canvas/backend/internal/model"
	"infinite-canvas/backend/internal/repository"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRegisterTaskOutputIsIdempotentForProjectRevision(t *testing.T) {
	svc, db := newProjectWorkflowTestService(t)
	seedProjectWorkflowTask(t, db)

	request := RegisterTaskOutputRequest{TaskID: "task-1", OutputJSON: `{"ok":true}`}
	step, err := svc.RegisterTaskOutput("user-1", "project-1", "step-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != model.WorkflowStepStatusCompleted {
		t.Fatalf("first registration status = %s", step.Status)
	}
	step, err = svc.RegisterTaskOutput("user-1", "project-1", "step-1", request)
	if err != nil {
		t.Fatal(err)
	}
	if step.Status != model.WorkflowStepStatusCompleted {
		t.Fatalf("duplicate registration returned status = %s", step.Status)
	}

	assertProjectWorkflowRegistrationState(t, db, 11, 5, 1)
}

func TestProjectWorkflowOutputReconciliationRepairsCanvasTaskOnce(t *testing.T) {
	svc, db := newProjectWorkflowTestService(t)
	seedProjectWorkflowTask(t, db)

	if recovered := svc.reconcileProjectTaskOutputs("user-1", "project-1"); !recovered {
		t.Fatal("expected the missing workflow output to be reconciled")
	}
	if recovered := svc.reconcileProjectTaskOutputs("user-1", "project-1"); recovered {
		t.Fatal("already registered task should not be reconciled twice")
	}

	assertProjectWorkflowRegistrationState(t, db, 11, 5, 1)
}

func TestRegisterTaskOutputFromTaskRejectsSpoofedDomainProject(t *testing.T) {
	svc, db := newProjectWorkflowTestService(t)
	seedProjectWorkflowTask(t, db)
	now := time.Now()
	otherProject := model.Project{ID: "project-2", UserID: "user-1", Name: "其他项目", Status: model.ProjectStatusActive, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&otherProject).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.Task{}).Where("id = ?", "task-1").Update("input_json", `{"workflowStepId":"step-1","domainProjectId":"project-2"}`).Error; err != nil {
		t.Fatal(err)
	}
	task, err := svc.repo.Task("task-1")
	if err != nil {
		t.Fatal(err)
	}
	if registered, err := svc.RegisterTaskOutputFromTask(*task); err == nil || registered {
		t.Fatalf("spoofed project registration = registered %t, error %v", registered, err)
	}
	var linkCount int64
	if err := db.Model(&model.WorkflowStepTask{}).Count(&linkCount).Error; err != nil {
		t.Fatal(err)
	}
	if linkCount != 0 {
		t.Fatalf("spoofed project created %d workflow links", linkCount)
	}
}

func newProjectWorkflowTestService(t *testing.T) (*Service, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&model.Project{},
		&model.CanvasProject{},
		&model.Task{},
		&model.TaskLog{},
		&model.WorkflowInstance{},
		&model.WorkflowStepInstance{},
		&model.WorkflowStepTask{},
	); err != nil {
		t.Fatal(err)
	}
	return &Service{repo: repository.New(db)}, db
}

func seedProjectWorkflowTask(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now()
	project := model.Project{ID: "project-1", UserID: "user-1", Name: "测试项目", Status: model.ProjectStatusActive, Revision: 10, CreatedAt: now, UpdatedAt: now}
	canvas := model.CanvasProject{ID: "canvas-1", UserID: "user-1", ProjectID: project.ID, Title: "测试画布", PayloadJSON: `{}`, CreatedAt: now, UpdatedAt: now}
	instance := model.WorkflowInstance{ID: "workflow-1", ProjectID: project.ID, TemplateVersionID: "template-1", Scope: "project", Status: model.WorkflowStatusActive, Revision: 4, CreatedAt: now, UpdatedAt: now}
	steps := []model.WorkflowStepInstance{
		{ID: "step-1", WorkflowInstanceID: instance.ID, StepKey: "generation", Name: "生成", Position: 0, Status: model.WorkflowStepStatusReady, InputJSON: `{}`, OutputJSON: `{}`, CreatedAt: now, UpdatedAt: now},
		{ID: "step-2", WorkflowInstanceID: instance.ID, StepKey: "delivery", Name: "交付", Position: 1, Status: model.WorkflowStepStatusPending, InputJSON: `{}`, OutputJSON: `{}`, CreatedAt: now, UpdatedAt: now},
	}
	task := model.Task{
		ID: "task-1", UserID: "user-1", ProjectID: canvas.ID, Type: "canvas_text", Status: model.TaskStatusSucceeded,
		InputJSON: `{"workflowStepId":"step-1","domainProjectId":"project-1"}`, ResultJSON: `{"ok":true}`,
		CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
	}
	for _, value := range []any{&project, &canvas, &instance, &steps, &task} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertProjectWorkflowRegistrationState(t *testing.T, db *gorm.DB, projectRevision int64, workflowRevision int64, linkCount int64) {
	t.Helper()
	var project model.Project
	var instance model.WorkflowInstance
	var firstStep model.WorkflowStepInstance
	var nextStep model.WorkflowStepInstance
	if err := db.First(&project, "id = ?", "project-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&instance, "id = ?", "workflow-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&firstStep, "id = ?", "step-1").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&nextStep, "id = ?", "step-2").Error; err != nil {
		t.Fatal(err)
	}
	var actualLinkCount int64
	if err := db.Model(&model.WorkflowStepTask{}).Count(&actualLinkCount).Error; err != nil {
		t.Fatal(err)
	}
	if project.Revision != projectRevision || instance.Revision != workflowRevision || actualLinkCount != linkCount {
		t.Fatalf("registration state = project revision %d, workflow revision %d, links %d", project.Revision, instance.Revision, actualLinkCount)
	}
	if firstStep.Status != model.WorkflowStepStatusCompleted || nextStep.Status != model.WorkflowStepStatusReady {
		t.Fatalf("workflow steps = first %s, next %s", firstStep.Status, nextStep.Status)
	}
}
