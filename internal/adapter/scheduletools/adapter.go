// Package scheduletools maps bounded model-facing plan operations to the
// Scheduler SDK. It never imports Scheduler implementation, storage, Runtime,
// Agent, Notification, or provider packages.
package scheduletools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	schedulersdk "github.com/domainry/domainry-scheduler-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

const (
	backgroundTaskKind = "background_task"
	followUpKind       = "follow_up"
	reminderKind       = "reminder"
	agentOwner         = "agent"
	agentOperation     = "conversation_task_start"
	reminderOwner      = "notification"
	reminderOperation  = "publish_reminder"
	reminderAction     = "notification.reminder.publish"
)

type Adapter struct {
	Plans      schedulersdk.ScheduledPlanService
	ProductKey string
	Catalog    sdk.Catalog
	Authorize  tools.Authorizer
}

func Definitions() []sdk.Definition { return sdk.ScheduleDefinitions() }

func (a *Adapter) Register(registry *tools.Registry) error {
	if a == nil || a.Plans == nil || strings.TrimSpace(a.ProductKey) == "" || a.Catalog == nil || a.Authorize == nil {
		return fmt.Errorf("schedule tool host configuration is incomplete")
	}
	for _, definition := range Definitions() {
		if err := registry.Register(tools.Registration{Definition: definition, Authorize: a.authorize, Invoke: a.Invoke, Reconcile: a.Reconcile, AuthorizeResult: a.AuthorizeResult}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) authorize(ctx context.Context, request sdk.Request) (sdk.Authorization, error) {
	if a == nil || a.Authorize == nil {
		return sdk.Authorization{}, nil
	}
	authorization, err := a.Authorize(ctx, request)
	if err != nil || !authorization.Granted {
		return authorization, err
	}
	authorization.Granted = a.Plans != nil && a.Catalog != nil && validAuthority(request.Authority) && validKey(a.ProductKey)
	return authorization, nil
}

func (a *Adapter) ConversationToolAvailable(_ context.Context, authority sdk.Authority, key string) (bool, error) {
	for _, definition := range Definitions() {
		if definition.Key == key {
			return a != nil && a.Plans != nil && a.Catalog != nil && a.Authorize != nil && validAuthority(authority) && validKey(a.ProductKey), nil
		}
	}
	return false, nil
}

type triggerInput struct {
	Type     string         `json:"type"`
	At       string         `json:"at,omitempty"`
	Schedule *scheduleInput `json:"schedule,omitempty"`
}

type scheduleInput struct {
	Type       string `json:"type"`
	TimeOfDay  string `json:"time_of_day"`
	DayOfWeek  string `json:"day_of_week,omitempty"`
	DayOfMonth int    `json:"day_of_month,omitempty"`
}

type detailsInput struct {
	Goal                *string   `json:"goal,omitempty"`
	Input               *string   `json:"input,omitempty"`
	AllowedTools        *[]string `json:"allowed_tools,omitempty"`
	CompletionCondition *string   `json:"completion_condition,omitempty"`
	Title               *string   `json:"title,omitempty"`
	Message             *string   `json:"message,omitempty"`
}

type createInput struct {
	Kind     string       `json:"kind"`
	Name     string       `json:"name"`
	Timezone string       `json:"timezone,omitempty"`
	Trigger  triggerInput `json:"trigger"`
	Details  detailsInput `json:"details"`
}

type listInput struct {
	Status string `json:"status,omitempty"`
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type planInput struct {
	PlanID           string `json:"plan_id"`
	ExpectedRevision int64  `json:"expected_revision,omitempty"`
}

type updateInput struct {
	PlanID           string        `json:"plan_id"`
	ExpectedRevision int64         `json:"expected_revision"`
	Name             *string       `json:"name,omitempty"`
	Timezone         *string       `json:"timezone,omitempty"`
	Trigger          *triggerInput `json:"trigger,omitempty"`
	Details          *detailsInput `json:"details,omitempty"`
}

type triggerView struct {
	Type     string        `json:"type"`
	At       string        `json:"at,omitempty"`
	Schedule *scheduleView `json:"schedule,omitempty"`
}

type scheduleView struct {
	Type       string `json:"type"`
	TimeOfDay  string `json:"time_of_day"`
	DayOfWeek  string `json:"day_of_week,omitempty"`
	DayOfMonth int    `json:"day_of_month,omitempty"`
}

type detailsView struct {
	Goal                string   `json:"goal,omitempty"`
	Input               string   `json:"input,omitempty"`
	AllowedTools        []string `json:"allowed_tools,omitempty"`
	CompletionCondition string   `json:"completion_condition,omitempty"`
	Title               string   `json:"title,omitempty"`
	Message             string   `json:"message,omitempty"`
}

type planView struct {
	ID             string      `json:"id"`
	Name           string      `json:"name"`
	Kind           string      `json:"kind"`
	Timezone       string      `json:"timezone"`
	Trigger        triggerView `json:"trigger"`
	Details        detailsView `json:"details"`
	Status         string      `json:"status"`
	Revision       int64       `json:"revision"`
	ConversationID string      `json:"conversation_id,omitempty"`
	RunID          string      `json:"run_id,omitempty"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type output struct {
	Operation     string      `json:"operation"`
	RequestSHA256 string      `json:"request_sha256"`
	Plan          *planView   `json:"plan,omitempty"`
	Items         *[]planView `json:"items,omitempty"`
	NextCursor    string      `json:"next_cursor,omitempty"`
	PlanID        string      `json:"plan_id,omitempty"`
	Revision      int64       `json:"revision,omitempty"`
	Deleted       bool        `json:"deleted,omitempty"`
	Replay        bool        `json:"replay,omitempty"`
}

func (a *Adapter) Invoke(ctx context.Context, request sdk.Request) (sdk.Result, error) {
	return a.invoke(ctx, request, false)
}

func (a *Adapter) Reconcile(ctx context.Context, request sdk.Request) (sdk.Result, error) {
	return a.invoke(ctx, request, true)
}

func (a *Adapter) invoke(ctx context.Context, request sdk.Request, reconcile bool) (sdk.Result, error) {
	if a == nil || a.Plans == nil || !validAuthority(request.Authority) {
		return sdk.Result{}, toolError("unavailable", "schedule_service_unavailable")
	}
	operation := strings.TrimPrefix(request.Definition.Key, "schedule_")
	out := output{Operation: operation, RequestSHA256: requestHash(request)}
	owner := a.owner(request.Authority)
	var err error
	switch request.Definition.Key {
	case sdk.ScheduleCreateToolKey:
		var input createInput
		if strictDecode(request.Call.Arguments, &input) != nil || strings.TrimSpace(request.IdempotencyKey) == "" {
			return sdk.Result{}, toolError("bad_request", "schedule_arguments_invalid")
		}
		authorization, authErr := a.authorize(ctx, request)
		if authErr != nil || !authorization.Granted {
			return sdk.Result{}, authErrOrDenied(authErr)
		}
		command, buildErr := a.createCommand(ctx, request, authorization, input)
		if buildErr != nil {
			return sdk.Result{}, buildErr
		}
		var receipt schedulersdk.ScheduledPlanReceipt
		receipt, err = a.Plans.CreateScheduledPlan(ctx, command)
		if err == nil {
			var view planView
			view, err = a.view(receipt.Plan)
			out.Plan, out.Replay = &view, receipt.Replay
		}
	case sdk.ScheduleListToolKey:
		var input listInput
		if strictDecode(request.Call.Arguments, &input) != nil {
			return sdk.Result{}, toolError("bad_request", "schedule_arguments_invalid")
		}
		var page schedulersdk.ScheduledPlanPage
		page, err = a.Plans.ListScheduledPlans(ctx, schedulersdk.ScheduledPlanList{Owner: owner, Status: input.Status, Cursor: input.Cursor, Limit: input.Limit})
		if err == nil {
			items, viewErr := a.views(page.Items, true)
			err = viewErr
			out.NextCursor = page.NextCursor
			if err == nil {
				if items == nil {
					items = []planView{}
				}
				out.Items = &items
			}
		}
	case sdk.ScheduleGetToolKey:
		var input planInput
		if strictDecode(request.Call.Arguments, &input) != nil {
			return sdk.Result{}, toolError("bad_request", "schedule_arguments_invalid")
		}
		var plan schedulersdk.ScheduledPlan
		plan, err = a.Plans.GetScheduledPlan(ctx, schedulersdk.ScheduledPlanLookup{Owner: owner, PlanID: input.PlanID})
		if err == nil {
			var view planView
			view, err = a.view(plan)
			out.Plan = &view
		}
	case sdk.ScheduleUpdateToolKey:
		var input updateInput
		if strictDecode(request.Call.Arguments, &input) != nil || input.Name == nil && input.Timezone == nil && input.Trigger == nil && input.Details == nil {
			return sdk.Result{}, toolError("bad_request", "schedule_arguments_invalid")
		}
		var receipt schedulersdk.ScheduledPlanReceipt
		receipt, err = a.update(ctx, request, input)
		if err == nil {
			var view planView
			view, err = a.view(receipt.Plan)
			out.Plan, out.Replay = &view, receipt.Replay
		}
	case sdk.SchedulePauseToolKey, sdk.ScheduleResumeToolKey, sdk.ScheduleDeleteToolKey:
		var input planInput
		if strictDecode(request.Call.Arguments, &input) != nil || input.ExpectedRevision < 1 {
			return sdk.Result{}, toolError("bad_request", "schedule_arguments_invalid")
		}
		lookup := schedulersdk.ScheduledPlanLookup{Owner: owner, PlanID: input.PlanID}
		plan, lookupErr := a.Plans.GetScheduledPlan(ctx, lookup)
		if lookupErr != nil && !(reconcile && request.Definition.Key == sdk.ScheduleDeleteToolKey && errors.Is(lookupErr, schedulersdk.ErrScheduledPlanNotFound)) {
			err = lookupErr
			break
		}
		if lookupErr == nil {
			if _, viewErr := a.view(plan); viewErr != nil {
				err = viewErr
				break
			}
		}
		change := schedulersdk.ScheduledPlanStatusChange{Owner: owner, PlanID: input.PlanID, ExpectedRevision: input.ExpectedRevision}
		switch request.Definition.Key {
		case sdk.SchedulePauseToolKey:
			var receipt schedulersdk.ScheduledPlanReceipt
			receipt, err = a.Plans.PauseScheduledPlan(ctx, change)
			if err == nil {
				var view planView
				view, err = a.view(receipt.Plan)
				out.Plan, out.Replay = &view, receipt.Replay
			}
		case sdk.ScheduleResumeToolKey:
			var receipt schedulersdk.ScheduledPlanReceipt
			receipt, err = a.Plans.ResumeScheduledPlan(ctx, change)
			if err == nil {
				var view planView
				view, err = a.view(receipt.Plan)
				out.Plan, out.Replay = &view, receipt.Replay
			}
		case sdk.ScheduleDeleteToolKey:
			var receipt schedulersdk.ScheduledPlanDeleteReceipt
			receipt, err = a.Plans.DeleteScheduledPlan(ctx, change)
			if err == nil {
				out.PlanID, out.Revision, out.Deleted, out.Replay = receipt.PlanID, receipt.Revision, receipt.Deleted, receipt.Replay
			}
		}
	default:
		return sdk.Result{}, toolError("bad_request", "schedule_operation_invalid")
	}
	if err != nil {
		return sdk.Result{}, mapSchedulerError(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return sdk.Result{}, err
	}
	return sdk.Result{Status: "completed", Content: raw, ResourceID: resourceID(out)}, nil
}

func (a *Adapter) createCommand(ctx context.Context, request sdk.Request, authorization sdk.Authorization, input createInput) (schedulersdk.ScheduledPlanCreate, error) {
	timezone := strings.TrimSpace(input.Timezone)
	if timezone == "" {
		timezone = strings.TrimSpace(authorization.UserTimezone)
	}
	trigger, err := schedulerTrigger(input.Trigger, timezone)
	if err != nil {
		return schedulersdk.ScheduledPlanCreate{}, err
	}
	command := schedulersdk.ScheduledPlanCreate{
		ClientID: "tool_" + stableHash(request.IdempotencyKey), Name: input.Name, Owner: a.owner(request.Authority), Timezone: timezone,
		Trigger: trigger, ConversationRef: schedulersdk.ScheduledPlanConversationRef{ConversationID: request.ConversationID, RunID: request.RunID},
	}
	switch input.Kind {
	case backgroundTaskKind:
		payload, actions, err := a.backgroundInput(ctx, request.Authority, input.Details, false)
		if err != nil {
			return schedulersdk.ScheduledPlanCreate{}, err
		}
		command.Input, command.AllowedActions = payload, actions
		command.Target = schedulersdk.TargetRef{Type: "runtime_operation", Owner: agentOwner, Operation: agentOperation}
	case followUpKind:
		payload, actions, err := a.backgroundInput(ctx, request.Authority, input.Details, true)
		if err != nil {
			return schedulersdk.ScheduledPlanCreate{}, err
		}
		command.Input, command.AllowedActions = payload, actions
		command.Target = schedulersdk.TargetRef{Type: "runtime_operation", Owner: agentOwner, Operation: agentOperation}
	case reminderKind:
		payload, err := reminderInput(input.Details)
		if err != nil {
			return schedulersdk.ScheduledPlanCreate{}, err
		}
		command.Input, command.AllowedActions = payload, []string{reminderAction}
		command.Target = schedulersdk.TargetRef{Type: "runtime_operation", Owner: reminderOwner, Operation: reminderOperation}
	default:
		return schedulersdk.ScheduledPlanCreate{}, toolError("bad_request", "schedule_kind_invalid")
	}
	return command, nil
}

func (a *Adapter) update(ctx context.Context, request sdk.Request, input updateInput) (schedulersdk.ScheduledPlanReceipt, error) {
	owner := a.owner(request.Authority)
	current, err := a.Plans.GetScheduledPlan(ctx, schedulersdk.ScheduledPlanLookup{Owner: owner, PlanID: input.PlanID})
	if err != nil {
		return schedulersdk.ScheduledPlanReceipt{}, err
	}
	view, err := a.view(current)
	if err != nil {
		return schedulersdk.ScheduledPlanReceipt{}, err
	}
	name, timezone, trigger := current.Name, current.Timezone, current.Trigger
	if input.Name != nil {
		name = *input.Name
	}
	if input.Timezone != nil {
		timezone = *input.Timezone
	}
	if input.Trigger != nil {
		trigger, err = schedulerTrigger(*input.Trigger, timezone)
		if err != nil {
			return schedulersdk.ScheduledPlanReceipt{}, err
		}
	} else if input.Timezone != nil && trigger.Schedule != nil {
		rule := *trigger.Schedule
		rule.Timezone = timezone
		trigger.Schedule = &rule
	}
	payload, actions := append(json.RawMessage(nil), current.Input...), append([]string(nil), current.AllowedActions...)
	if input.Details != nil {
		details := view.Details
		mergeDetails(&details, *input.Details)
		switch view.Kind {
		case backgroundTaskKind:
			payload, actions, err = a.backgroundInput(ctx, request.Authority, details.asInput(), false)
		case followUpKind:
			payload, actions, err = a.backgroundInput(ctx, request.Authority, details.asInput(), true)
		case reminderKind:
			payload, err = reminderInput(details.asInput())
			actions = []string{reminderAction}
		}
		if err != nil {
			return schedulersdk.ScheduledPlanReceipt{}, err
		}
	}
	conversation := current.ConversationRef
	if request.ConversationID != "" {
		conversation = schedulersdk.ScheduledPlanConversationRef{ConversationID: request.ConversationID, RunID: request.RunID}
	}
	return a.Plans.UpdateScheduledPlan(ctx, schedulersdk.ScheduledPlanUpdate{
		Owner: owner, PlanID: input.PlanID, ExpectedRevision: input.ExpectedRevision, Name: name, Timezone: timezone,
		Trigger: trigger, Input: payload, AllowedActions: actions, Target: current.Target, ConversationRef: conversation,
	})
}

func (a *Adapter) backgroundInput(ctx context.Context, authority sdk.Authority, details detailsInput, followUp bool) (json.RawMessage, []string, error) {
	if details.Goal == nil || details.AllowedTools == nil || details.Title != nil || details.Message != nil || strings.TrimSpace(*details.Goal) == "" || len(*details.AllowedTools) == 0 ||
		(followUp && (details.CompletionCondition == nil || strings.TrimSpace(*details.CompletionCondition) == "")) || (!followUp && details.CompletionCondition != nil) {
		return nil, nil, toolError("bad_request", "schedule_task_details_invalid")
	}
	definitions, err := a.Catalog.ConversationTools(ctx, authority)
	if err != nil {
		return nil, nil, err
	}
	available := make(map[string]sdk.Definition, len(definitions))
	for _, definition := range definitions {
		if strings.HasPrefix(definition.Key, "task_") || strings.HasPrefix(definition.Key, "schedule_") {
			continue
		}
		available[definition.Key] = definition
	}
	tools := append([]string(nil), (*details.AllowedTools)...)
	sort.Strings(tools)
	actions := make([]string, 0, len(tools))
	for index, key := range tools {
		definition, found := available[key]
		if !found || !validKey(key) || index > 0 && tools[index-1] == key {
			return nil, nil, toolError("forbidden", "schedule_task_tool_denied")
		}
		actions = append(actions, definition.ActionKey)
	}
	payload := struct {
		Goal         string         `json:"goal"`
		Input        string         `json:"input,omitempty"`
		AllowedTools []string       `json:"allowed_tools"`
		FollowUp     map[string]any `json:"follow_up,omitempty"`
	}{Goal: strings.TrimSpace(*details.Goal), AllowedTools: tools}
	if details.Input != nil {
		payload.Input = strings.TrimSpace(*details.Input)
	}
	if followUp {
		payload.FollowUp = map[string]any{"completion_condition": strings.TrimSpace(*details.CompletionCondition)}
	}
	raw, _ := json.Marshal(payload)
	return raw, actions, nil
}

func reminderInput(details detailsInput) (json.RawMessage, error) {
	if details.Title == nil || details.Message == nil || details.Goal != nil || details.Input != nil || details.AllowedTools != nil || strings.TrimSpace(*details.Title) == "" || strings.TrimSpace(*details.Message) == "" {
		return nil, toolError("bad_request", "schedule_reminder_details_invalid")
	}
	raw, _ := json.Marshal(map[string]string{"title": strings.TrimSpace(*details.Title), "message": strings.TrimSpace(*details.Message)})
	return raw, nil
}

func schedulerTrigger(input triggerInput, timezone string) (schedulersdk.ScheduledPlanTrigger, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_timezone_required")
	}
	switch input.Type {
	case schedulersdk.ScheduledPlanTriggerOnce:
		if input.Schedule != nil || strings.TrimSpace(input.At) == "" {
			return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
		}
		at, err := time.Parse(time.RFC3339Nano, input.At)
		if err != nil {
			return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
		}
		at = at.UTC()
		return schedulersdk.ScheduledPlanTrigger{Type: schedulersdk.ScheduledPlanTriggerOnce, At: &at}, nil
	case schedulersdk.ScheduledPlanTriggerRecurring:
		if input.Schedule == nil || input.At != "" {
			return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
		}
		rule := schedulersdk.Schedule{Type: input.Schedule.Type, TimeOfDay: input.Schedule.TimeOfDay, DayOfWeek: input.Schedule.DayOfWeek, DayOfMonth: input.Schedule.DayOfMonth, Timezone: timezone}
		switch rule.Type {
		case "daily_at":
			if rule.DayOfWeek != "" || rule.DayOfMonth != 0 {
				return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
			}
		case "weekly_at":
			if rule.DayOfWeek == "" || rule.DayOfMonth != 0 {
				return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
			}
		case "monthly_at":
			if rule.DayOfWeek != "" || rule.DayOfMonth < 1 {
				return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
			}
		default:
			return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
		}
		return schedulersdk.ScheduledPlanTrigger{Type: schedulersdk.ScheduledPlanTriggerRecurring, Schedule: &rule}, nil
	default:
		return schedulersdk.ScheduledPlanTrigger{}, toolError("bad_request", "schedule_trigger_invalid")
	}
}

func (a *Adapter) view(plan schedulersdk.ScheduledPlan) (planView, error) {
	view := planView{ID: plan.ID, Name: plan.Name, Timezone: plan.Timezone, Status: plan.Status, Revision: plan.Revision, ConversationID: plan.ConversationRef.ConversationID, RunID: plan.ConversationRef.RunID, CreatedAt: plan.CreatedAt, UpdatedAt: plan.UpdatedAt}
	if plan.Trigger.Type == schedulersdk.ScheduledPlanTriggerOnce && plan.Trigger.At != nil {
		view.Trigger = triggerView{Type: schedulersdk.ScheduledPlanTriggerOnce, At: plan.Trigger.At.UTC().Format(time.RFC3339Nano)}
	} else if plan.Trigger.Type == schedulersdk.ScheduledPlanTriggerRecurring && plan.Trigger.Schedule != nil {
		rule := plan.Trigger.Schedule
		view.Trigger = triggerView{Type: schedulersdk.ScheduledPlanTriggerRecurring, Schedule: &scheduleView{Type: rule.Type, TimeOfDay: rule.TimeOfDay, DayOfWeek: rule.DayOfWeek, DayOfMonth: rule.DayOfMonth}}
	} else {
		return planView{}, toolError("unavailable", "schedule_record_invalid")
	}
	var err error
	switch {
	case normalizedTarget(plan.Target, agentOwner, agentOperation):
		var details struct {
			Goal         string   `json:"goal"`
			Input        string   `json:"input,omitempty"`
			AllowedTools []string `json:"allowed_tools"`
			FollowUp     *struct {
				CompletionCondition string `json:"completion_condition"`
			} `json:"follow_up,omitempty"`
		}
		if strictDecode(string(plan.Input), &details) != nil || strings.TrimSpace(details.Goal) == "" || len(details.AllowedTools) == 0 {
			err = toolError("unavailable", "schedule_record_invalid")
		} else {
			view.Kind = backgroundTaskKind
			view.Details = detailsView{Goal: details.Goal, Input: details.Input, AllowedTools: append([]string(nil), details.AllowedTools...)}
			if details.FollowUp != nil {
				if strings.TrimSpace(details.FollowUp.CompletionCondition) == "" {
					return planView{}, toolError("unavailable", "schedule_record_invalid")
				}
				view.Kind = followUpKind
				view.Details.CompletionCondition = details.FollowUp.CompletionCondition
			}
		}
	case normalizedTarget(plan.Target, reminderOwner, reminderOperation) && len(plan.AllowedActions) == 1 && plan.AllowedActions[0] == reminderAction:
		view.Kind = reminderKind
		var details struct {
			Title   string `json:"title"`
			Message string `json:"message"`
		}
		if strictDecode(string(plan.Input), &details) != nil || strings.TrimSpace(details.Title) == "" || strings.TrimSpace(details.Message) == "" {
			err = toolError("unavailable", "schedule_record_invalid")
		} else {
			view.Details = detailsView{Title: details.Title, Message: details.Message}
		}
	default:
		err = toolError("not_found", "schedule_not_managed")
	}
	return view, err
}

func (a *Adapter) views(plans []schedulersdk.ScheduledPlan, skipUnmanaged bool) ([]planView, error) {
	views := make([]planView, 0, len(plans))
	for _, plan := range plans {
		view, err := a.view(plan)
		if err != nil {
			var tool *sdk.Error
			if skipUnmanaged && errors.As(err, &tool) && tool.Code == "schedule_not_managed" {
				continue
			}
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (a *Adapter) AuthorizeResult(ctx context.Context, request sdk.Request, result sdk.Result) error {
	if result.Status != "completed" || len(result.Content) == 0 || len(result.Content) > 65536 {
		return toolError("forbidden", "schedule_result_invalid")
	}
	var saved output
	if strictDecode(string(result.Content), &saved) != nil || saved.Operation != strings.TrimPrefix(request.Definition.Key, "schedule_") || saved.RequestSHA256 != requestHash(request) {
		return toolError("forbidden", "schedule_result_invalid")
	}
	owner := a.owner(request.Authority)
	switch request.Definition.Key {
	case sdk.ScheduleListToolKey:
		var input listInput
		if strictDecode(request.Call.Arguments, &input) != nil {
			return toolError("forbidden", "schedule_result_invalid")
		}
		page, err := a.Plans.ListScheduledPlans(ctx, schedulersdk.ScheduledPlanList{Owner: owner, Status: input.Status, Cursor: input.Cursor, Limit: input.Limit})
		if err != nil {
			return err
		}
		views, err := a.views(page.Items, true)
		if err != nil || saved.Items == nil || digest(views) != digest(*saved.Items) || page.NextCursor != saved.NextCursor {
			return toolError("forbidden", "schedule_result_changed")
		}
		return nil
	case sdk.ScheduleDeleteToolKey:
		var input planInput
		if strictDecode(request.Call.Arguments, &input) != nil || saved.PlanID != input.PlanID || saved.Revision != input.ExpectedRevision+1 || !saved.Deleted {
			return toolError("forbidden", "schedule_result_invalid")
		}
		_, err := a.Plans.GetScheduledPlan(ctx, schedulersdk.ScheduledPlanLookup{Owner: owner, PlanID: input.PlanID})
		if errors.Is(err, schedulersdk.ErrScheduledPlanNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		return toolError("forbidden", "schedule_result_changed")
	default:
		if saved.Plan == nil {
			return toolError("forbidden", "schedule_result_invalid")
		}
		current, err := a.Plans.GetScheduledPlan(ctx, schedulersdk.ScheduledPlanLookup{Owner: owner, PlanID: saved.Plan.ID})
		if err != nil {
			return err
		}
		view, err := a.view(current)
		if err != nil || digest(view) != digest(*saved.Plan) {
			return toolError("forbidden", "schedule_result_changed")
		}
		return nil
	}
}

func (a *Adapter) owner(authority sdk.Authority) schedulersdk.ScheduledPlanOwner {
	return schedulersdk.ScheduledPlanOwner{WorkspaceID: authority.WorkspaceID, UserID: authority.UserID, ProductKey: strings.TrimSpace(a.ProductKey)}
}

func normalizedTarget(target schedulersdk.TargetRef, owner, operation string) bool {
	target = target.Normalize()
	return target.Type == "runtime_operation" && target.Owner == owner && target.Operation == operation && target.ConnectionKey == "" && len(target.Payload) == 0
}

func mergeDetails(current *detailsView, patch detailsInput) {
	if patch.Goal != nil {
		current.Goal = *patch.Goal
	}
	if patch.Input != nil {
		current.Input = *patch.Input
	}
	if patch.AllowedTools != nil {
		current.AllowedTools = append([]string(nil), (*patch.AllowedTools)...)
	}
	if patch.CompletionCondition != nil {
		current.CompletionCondition = *patch.CompletionCondition
	}
	if patch.Title != nil {
		current.Title = *patch.Title
	}
	if patch.Message != nil {
		current.Message = *patch.Message
	}
}

func (d detailsView) asInput() detailsInput {
	result := detailsInput{}
	if d.Goal != "" || d.AllowedTools != nil {
		result.Goal, result.Input, result.AllowedTools = &d.Goal, &d.Input, &d.AllowedTools
	}
	if d.CompletionCondition != "" {
		result.CompletionCondition = &d.CompletionCondition
	}
	if d.Title != "" || d.Message != "" {
		result.Title, result.Message = &d.Title, &d.Message
	}
	return result
}

func validAuthority(authority sdk.Authority) bool {
	return authority.Known && validKey(authority.RuntimeID) && validKey(authority.WorkspaceID) && validKey(authority.UserID)
}

func validKey(value string) bool {
	if value == "" || len(value) > 191 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character < 0x21 || character == 0x7f {
			return false
		}
	}
	return true
}

func strictDecode(raw string, value any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("trailing JSON")
	}
	return nil
}

func stableHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func requestHash(request sdk.Request) string {
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(request.Call.Arguments)) != nil {
		compact.WriteString(request.Call.Arguments)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{request.Definition.Key, request.Definition.Version, compact.String()}, "\x00")))
	return hex.EncodeToString(sum[:])
}

func digest(value any) string {
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func resourceID(out output) string {
	if out.Plan != nil {
		return out.Plan.ID
	}
	return out.PlanID
}

func authErrOrDenied(err error) error {
	if err != nil {
		return err
	}
	return toolError("forbidden", "schedule_access_denied")
}

func mapSchedulerError(err error) error {
	switch {
	case errors.Is(err, schedulersdk.ErrScheduledPlanInvalid):
		return toolError("bad_request", "schedule_invalid")
	case errors.Is(err, schedulersdk.ErrScheduledPlanConflict):
		return toolError("conflict", "schedule_revision_conflict")
	case errors.Is(err, schedulersdk.ErrScheduledPlanNotFound):
		return toolError("not_found", "schedule_not_found")
	default:
		return toolError("unavailable", "schedule_service_unavailable")
	}
}

func toolError(class, code string) error { return &sdk.Error{Class: class, Code: code} }

var _ sdk.Availability = (*Adapter)(nil)
