package scheduletools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	schedulersdk "github.com/domainry/domainry-scheduler-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type planServiceStub struct {
	plans map[string]schedulersdk.ScheduledPlan
	last  schedulersdk.ScheduledPlanCreate
}

func (s *planServiceStub) CreateScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanCreate) (schedulersdk.ScheduledPlanReceipt, error) {
	if s.plans == nil {
		s.plans = map[string]schedulersdk.ScheduledPlan{}
	}
	id := "plan-" + input.ClientID
	if plan, found := s.plans[id]; found {
		return schedulersdk.ScheduledPlanReceipt{Plan: plan, Replay: true}, nil
	}
	s.last = input
	now := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	plan := schedulersdk.ScheduledPlan{ID: id, Name: input.Name, Owner: input.Owner, Timezone: input.Timezone, Trigger: input.Trigger, Input: input.Input, AllowedActions: input.AllowedActions, Target: input.Target, ConversationRef: input.ConversationRef, Status: schedulersdk.ScheduledPlanStatusEnabled, Revision: 1, CreatedAt: now, UpdatedAt: now}
	s.plans[id] = plan
	return schedulersdk.ScheduledPlanReceipt{Plan: plan}, nil
}
func (s *planServiceStub) GetScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanLookup) (schedulersdk.ScheduledPlan, error) {
	plan, found := s.plans[input.PlanID]
	if !found || plan.Owner != input.Owner || plan.Status == schedulersdk.ScheduledPlanStatusDeleted {
		return schedulersdk.ScheduledPlan{}, schedulersdk.ErrScheduledPlanNotFound
	}
	return plan, nil
}
func (s *planServiceStub) ListScheduledPlans(_ context.Context, input schedulersdk.ScheduledPlanList) (schedulersdk.ScheduledPlanPage, error) {
	page := schedulersdk.ScheduledPlanPage{Items: []schedulersdk.ScheduledPlan{}}
	for _, plan := range s.plans {
		if plan.Owner == input.Owner && plan.Status != schedulersdk.ScheduledPlanStatusDeleted && (input.Status == "" || input.Status == plan.Status) {
			page.Items = append(page.Items, plan)
		}
	}
	return page, nil
}
func (s *planServiceStub) UpdateScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanUpdate) (schedulersdk.ScheduledPlanReceipt, error) {
	plan, err := s.GetScheduledPlan(context.Background(), schedulersdk.ScheduledPlanLookup{Owner: input.Owner, PlanID: input.PlanID})
	if err != nil {
		return schedulersdk.ScheduledPlanReceipt{}, err
	}
	if plan.Revision != input.ExpectedRevision {
		return schedulersdk.ScheduledPlanReceipt{}, schedulersdk.ErrScheduledPlanConflict
	}
	plan.Name, plan.Timezone, plan.Trigger, plan.Input, plan.AllowedActions, plan.Target, plan.ConversationRef = input.Name, input.Timezone, input.Trigger, input.Input, input.AllowedActions, input.Target, input.ConversationRef
	plan.Revision++
	s.plans[plan.ID] = plan
	return schedulersdk.ScheduledPlanReceipt{Plan: plan}, nil
}
func (s *planServiceStub) status(input schedulersdk.ScheduledPlanStatusChange, status string) (schedulersdk.ScheduledPlanReceipt, error) {
	plan, err := s.GetScheduledPlan(context.Background(), schedulersdk.ScheduledPlanLookup{Owner: input.Owner, PlanID: input.PlanID})
	if err != nil {
		return schedulersdk.ScheduledPlanReceipt{}, err
	}
	if plan.Revision != input.ExpectedRevision {
		return schedulersdk.ScheduledPlanReceipt{}, schedulersdk.ErrScheduledPlanConflict
	}
	plan.Status, plan.Revision = status, plan.Revision+1
	s.plans[plan.ID] = plan
	return schedulersdk.ScheduledPlanReceipt{Plan: plan}, nil
}
func (s *planServiceStub) PauseScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanStatusChange) (schedulersdk.ScheduledPlanReceipt, error) {
	return s.status(input, schedulersdk.ScheduledPlanStatusPaused)
}
func (s *planServiceStub) ResumeScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanStatusChange) (schedulersdk.ScheduledPlanReceipt, error) {
	return s.status(input, schedulersdk.ScheduledPlanStatusEnabled)
}
func (s *planServiceStub) DeleteScheduledPlan(_ context.Context, input schedulersdk.ScheduledPlanStatusChange) (schedulersdk.ScheduledPlanDeleteReceipt, error) {
	if plan, found := s.plans[input.PlanID]; found && plan.Owner == input.Owner && plan.Status == schedulersdk.ScheduledPlanStatusDeleted {
		if plan.Revision == input.ExpectedRevision+1 {
			return schedulersdk.ScheduledPlanDeleteReceipt{PlanID: input.PlanID, Revision: plan.Revision, Deleted: true, Replay: true}, nil
		}
		return schedulersdk.ScheduledPlanDeleteReceipt{}, schedulersdk.ErrScheduledPlanNotFound
	}
	receipt, err := s.status(input, schedulersdk.ScheduledPlanStatusDeleted)
	return schedulersdk.ScheduledPlanDeleteReceipt{PlanID: input.PlanID, Revision: receipt.Plan.Revision, Deleted: err == nil}, err
}

type catalogStub []sdk.Definition

func (c catalogStub) ConversationTools(context.Context, sdk.Authority) ([]sdk.Definition, error) {
	return append([]sdk.Definition(nil), c...), nil
}

func scheduleFixture(t *testing.T) (*planServiceStub, *tools.Selection, sdk.Authority) {
	t.Helper()
	plans := &planServiceStub{}
	base := catalogStub{
		{Key: "todo_list", Version: "1", ActionKey: "agent.conversation_tools.todo_list"},
		{Key: "task_start", Version: "1", ActionKey: "agent.conversation_tools.task_start"},
	}
	adapter := &Adapter{Plans: plans, ProductKey: "work", Catalog: base, Authorize: func(_ context.Context, request sdk.Request) (sdk.Authorization, error) {
		return sdk.Authorization{Granted: request.Authority.Known, UserTimezone: "Asia/Shanghai", Revision: "auth-1"}, nil
	}}
	registry := tools.NewRegistry()
	if err := adapter.Register(registry); err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, definition := range Definitions() {
		keys = append(keys, definition.Key)
	}
	selection, err := registry.Select(keys)
	if err != nil {
		t.Fatal(err)
	}
	return plans, selection, sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "user"}
}

func invokeSchedule(t *testing.T, host *tools.Selection, authority sdk.Authority, key, arguments string, revision int) sdk.Result {
	t.Helper()
	var definition sdk.Definition
	for _, candidate := range Definitions() {
		if candidate.Key == key {
			definition = candidate
		}
	}
	result, err := host.InvokeConversationTool(t.Context(), sdk.Request{Authority: authority, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: fmt.Sprintf("call-%d", revision), Name: key, Arguments: arguments}, Definition: definition, IdempotencyKey: fmt.Sprintf("idempotency-%d", revision)})
	if err != nil {
		t.Fatalf("invoke %s: %v", key, err)
	}
	if result.Status != "completed" {
		t.Fatalf("invoke %s result=%+v", key, result)
	}
	return result
}

func TestScheduleToolsMapNaturalLanguagePlansAndManageLifecycle(t *testing.T) {
	plans, host, authority := scheduleFixture(t)
	created := invokeSchedule(t, host, authority, sdk.ScheduleCreateToolKey, `{"kind":"background_task","name":"每周一整理待办","trigger":{"type":"recurring","schedule":{"type":"weekly_at","time_of_day":"09:00","day_of_week":"monday"}},"details":{"goal":"整理本周待办","allowed_tools":["todo_list"]}}`, 1)
	if plans.last.Owner != (schedulersdk.ScheduledPlanOwner{WorkspaceID: "workspace", UserID: "user", ProductKey: "work"}) || plans.last.Timezone != "Asia/Shanghai" || plans.last.Target.Owner != agentOwner || plans.last.Target.Operation != agentOperation || len(plans.last.AllowedActions) != 1 || plans.last.AllowedActions[0] != "agent.conversation_tools.todo_list" || plans.last.ConversationRef.ConversationID != "conversation" {
		t.Fatalf("background command=%+v", plans.last)
	}
	var createOut output
	if err := json.Unmarshal(created.Content, &createOut); err != nil || createOut.Plan == nil || createOut.Plan.Kind != backgroundTaskKind {
		t.Fatalf("create output=%s err=%v", created.Content, err)
	}
	planID := createOut.Plan.ID
	listed := invokeSchedule(t, host, authority, sdk.ScheduleListToolKey, `{}`, 2)
	var listOut output
	_ = json.Unmarshal(listed.Content, &listOut)
	if listOut.Items == nil || len(*listOut.Items) != 1 || (*listOut.Items)[0].ID != planID {
		t.Fatalf("list=%s", listed.Content)
	}
	updated := invokeSchedule(t, host, authority, sdk.ScheduleUpdateToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":1,"name":"每周二整理待办","trigger":{"type":"recurring","schedule":{"type":"weekly_at","time_of_day":"10:30","day_of_week":"tuesday"}}}`, planID), 3)
	var updateOut output
	_ = json.Unmarshal(updated.Content, &updateOut)
	if updateOut.Plan == nil || updateOut.Plan.Revision != 2 || updateOut.Plan.Trigger.Schedule.DayOfWeek != "tuesday" {
		t.Fatalf("update=%s", updated.Content)
	}
	paused := invokeSchedule(t, host, authority, sdk.SchedulePauseToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":2}`, planID), 4)
	var pauseOut output
	_ = json.Unmarshal(paused.Content, &pauseOut)
	if pauseOut.Plan == nil || pauseOut.Plan.Status != schedulersdk.ScheduledPlanStatusPaused || pauseOut.Plan.Revision != 3 {
		t.Fatalf("pause=%s", paused.Content)
	}
	resumed := invokeSchedule(t, host, authority, sdk.ScheduleResumeToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":3}`, planID), 5)
	var resumeOut output
	_ = json.Unmarshal(resumed.Content, &resumeOut)
	if resumeOut.Plan == nil || resumeOut.Plan.Status != schedulersdk.ScheduledPlanStatusEnabled || resumeOut.Plan.Revision != 4 {
		t.Fatalf("resume=%s", resumed.Content)
	}
	deleted := invokeSchedule(t, host, authority, sdk.ScheduleDeleteToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":4}`, planID), 6)
	var deleteOut output
	_ = json.Unmarshal(deleted.Content, &deleteOut)
	if !deleteOut.Deleted || deleteOut.Revision != 5 {
		t.Fatalf("delete=%s", deleted.Content)
	}
	deleteDefinition := Definitions()[6]
	replayedDelete, err := host.ReconcileConversationTool(t.Context(), sdk.Request{Authority: authority, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: "call-6", Name: sdk.ScheduleDeleteToolKey, Arguments: fmt.Sprintf(`{"plan_id":%q,"expected_revision":4}`, planID)}, Definition: deleteDefinition, IdempotencyKey: "idempotency-6"})
	if err != nil {
		t.Fatal("reconcile delete", err)
	}
	deleteOut = output{}
	_ = json.Unmarshal(replayedDelete.Content, &deleteOut)
	if !deleteOut.Deleted || !deleteOut.Replay || deleteOut.Revision != 5 {
		t.Fatalf("delete replay=%s", replayedDelete.Content)
	}
	if _, err := host.ReconcileConversationTool(t.Context(), sdk.Request{Authority: authority, Call: sdk.Call{ID: "missing", Name: sdk.ScheduleDeleteToolKey, Arguments: `{"plan_id":"missing-plan","expected_revision":1}`}, Definition: deleteDefinition, IdempotencyKey: "missing-delete"}); err == nil {
		t.Fatal("missing delete was synthesized as a successful replay")
	}
	listed = invokeSchedule(t, host, authority, sdk.ScheduleListToolKey, `{}`, 7)
	listOut = output{}
	_ = json.Unmarshal(listed.Content, &listOut)
	if listOut.Items == nil || len(*listOut.Items) != 0 {
		t.Fatalf("deleted list=%s", listed.Content)
	}
}

func TestScheduleToolsBuildReminderAndRejectRoutingOrUnavailableTaskTools(t *testing.T) {
	plans, host, authority := scheduleFixture(t)
	created := invokeSchedule(t, host, authority, sdk.ScheduleCreateToolKey, `{"kind":"reminder","name":"周五提醒","trigger":{"type":"recurring","schedule":{"type":"weekly_at","time_of_day":"17:00","day_of_week":"friday"}},"details":{"title":"周报","message":"提交周报"}}`, 1)
	if plans.last.Target.Owner != reminderOwner || plans.last.Target.Operation != reminderOperation || len(plans.last.AllowedActions) != 1 || plans.last.AllowedActions[0] != reminderAction || string(plans.last.Input) != `{"message":"提交周报","title":"周报"}` {
		t.Fatalf("reminder command=%+v", plans.last)
	}
	var out output
	_ = json.Unmarshal(created.Content, &out)
	if out.Plan == nil || out.Plan.Kind != reminderKind {
		t.Fatalf("reminder output=%s", created.Content)
	}
	definition := Definitions()[0]
	for _, arguments := range []string{
		`{"kind":"reminder","name":"bad","owner":{"user_id":"other"},"trigger":{"type":"once","at":"2026-09-18T09:00:00Z"},"details":{"title":"x","message":"y"}}`,
		`{"kind":"background_task","name":"bad","trigger":{"type":"once","at":"2026-09-18T09:00:00Z"},"details":{"goal":"x","allowed_tools":["task_start"]}}`,
		`{"kind":"background_task","name":"bad","trigger":{"type":"once","at":"2026-09-18T09:00:00Z"},"details":{"goal":"x","allowed_tools":["mail_send"]}}`,
	} {
		_, err := host.InvokeConversationTool(t.Context(), sdk.Request{Authority: authority, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: "bad", Name: definition.Key, Arguments: arguments}, Definition: definition, IdempotencyKey: "bad"})
		if err == nil {
			t.Fatalf("invalid schedule accepted: %s", arguments)
		}
	}
	other := authority
	other.UserID = "other"
	result := invokeSchedule(t, host, other, sdk.ScheduleListToolKey, `{}`, 9)
	var list output
	_ = json.Unmarshal(result.Content, &list)
	if list.Items == nil || len(*list.Items) != 0 {
		t.Fatalf("cross-user list=%s", result.Content)
	}
	if _, err := plans.GetScheduledPlan(t.Context(), schedulersdk.ScheduledPlanLookup{Owner: schedulersdk.ScheduledPlanOwner{WorkspaceID: "workspace", UserID: "other", ProductKey: "work"}, PlanID: out.Plan.ID}); !errors.Is(err, schedulersdk.ErrScheduledPlanNotFound) {
		t.Fatalf("cross-user get err=%v", err)
	}
}

func TestScheduleToolsBuildScopedFollowUpAndPreserveItAcrossUpdate(t *testing.T) {
	plans, host, authority := scheduleFixture(t)
	created := invokeSchedule(t, host, authority, sdk.ScheduleCreateToolKey, `{"kind":"follow_up","name":"跟进发布阻塞项","trigger":{"type":"recurring","schedule":{"type":"daily_at","time_of_day":"09:00"}},"details":{"goal":"检查发布阻塞项","input":"只看本周发布","allowed_tools":["todo_list"],"completion_condition":"发布阻塞项全部完成"}}`, 20)
	var out output
	if err := json.Unmarshal(created.Content, &out); err != nil || out.Plan == nil || out.Plan.Kind != followUpKind || out.Plan.Details.CompletionCondition != "发布阻塞项全部完成" {
		t.Fatalf("follow-up output=%s err=%v", created.Content, err)
	}
	var payload struct {
		Goal         string   `json:"goal"`
		AllowedTools []string `json:"allowed_tools"`
		FollowUp     struct {
			CompletionCondition string `json:"completion_condition"`
		} `json:"follow_up"`
	}
	if err := json.Unmarshal(plans.last.Input, &payload); err != nil || payload.FollowUp.CompletionCondition != "发布阻塞项全部完成" || len(payload.AllowedTools) != 1 || payload.AllowedTools[0] != "todo_list" {
		t.Fatalf("follow-up scheduler payload=%s err=%v", plans.last.Input, err)
	}
	updated := invokeSchedule(t, host, authority, sdk.ScheduleUpdateToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":1,"details":{"completion_condition":"发布已上线"}}`, out.Plan.ID), 21)
	var updatedOut output
	if err := json.Unmarshal(updated.Content, &updatedOut); err != nil || updatedOut.Plan == nil || updatedOut.Plan.Kind != followUpKind || updatedOut.Plan.Details.CompletionCondition != "发布已上线" {
		t.Fatalf("updated follow-up=%s err=%v", updated.Content, err)
	}
	paused := invokeSchedule(t, host, authority, sdk.SchedulePauseToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":2}`, out.Plan.ID), 22)
	var pausedOut output
	if err := json.Unmarshal(paused.Content, &pausedOut); err != nil || pausedOut.Plan == nil || pausedOut.Plan.Kind != followUpKind || pausedOut.Plan.Status != schedulersdk.ScheduledPlanStatusPaused {
		t.Fatalf("paused follow-up=%s err=%v", paused.Content, err)
	}
	definition := Definitions()[0]
	for index, arguments := range []string{
		`{"kind":"follow_up","name":"bad","trigger":{"type":"recurring","schedule":{"type":"daily_at","time_of_day":"09:00"}},"details":{"goal":"x","allowed_tools":["todo_list"]}}`,
		`{"kind":"background_task","name":"bad","trigger":{"type":"recurring","schedule":{"type":"daily_at","time_of_day":"09:00"}},"details":{"goal":"x","allowed_tools":["todo_list"],"completion_condition":"done"}}`,
	} {
		_, err := host.InvokeConversationTool(t.Context(), sdk.Request{Authority: authority, ConversationID: "conversation", RunID: "run", Call: sdk.Call{ID: fmt.Sprintf("bad-%d", index), Name: definition.Key, Arguments: arguments}, Definition: definition, IdempotencyKey: fmt.Sprintf("bad-%d", index)})
		if err == nil {
			t.Fatalf("invalid follow-up %d was accepted", index)
		}
	}
}

var _ schedulersdk.ScheduledPlanService = (*planServiceStub)(nil)
