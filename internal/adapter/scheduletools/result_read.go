package scheduletools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	scheduler "github.com/domainry/domainry-scheduler-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
)

// AuthorizeResultRead checks current plan data access independently of the
// producer's mutation grant. The caller attests the immutable execution record;
// this adapter checks the original result against the current owner resource.
func (a *Adapter) AuthorizeResultRead(ctx context.Context, request sdk.Request, result sdk.Result) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a == nil || a.Plans == nil || a.Authorize == nil || !validAuthority(request.Authority) || !validKey(a.ProductKey) {
		return toolError("forbidden", "schedule_access_denied")
	}
	var saved output
	if result.Status != "completed" || result.ErrorCode != "" || len(result.Content) == 0 || len(result.Content) > 65536 || strictDecode(string(result.Content), &saved) != nil || saved.Operation != strings.TrimPrefix(request.Definition.Key, "schedule_") || saved.RequestSHA256 != requestHash(request) || result.ResourceID != resourceID(saved) {
		return toolError("forbidden", "schedule_result_invalid")
	}
	switch request.Definition.Key {
	case sdk.ScheduleListToolKey:
		var input listInput
		if strictDecode(request.Call.Arguments, &input) != nil || saved.Items == nil || saved.Plan != nil || saved.PlanID != "" || saved.Revision != 0 || saved.Deleted || saved.Replay || len(*saved.Items) > 100 || input.Limit > 0 && len(*saved.Items) > input.Limit {
			return toolError("forbidden", "schedule_result_invalid")
		}
		if err := a.authorizePlanRead(ctx, request, sdk.ScheduleListToolKey, input); err != nil {
			return err
		}
		previous := input.Cursor
		for _, plan := range *saved.Items {
			if plan.ID <= previous || input.Status != "" && plan.Status != input.Status {
				return toolError("forbidden", "schedule_result_invalid")
			}
			previous = plan.ID
			if err := a.readSavedPlan(ctx, request, plan); err != nil {
				return err
			}
		}
		// This is the saved page. New plans cannot replace its items/cursor.
		return ctx.Err()
	case sdk.ScheduleDeleteToolKey:
		var input planInput
		if strictDecode(request.Call.Arguments, &input) != nil || input.ExpectedRevision < 1 || saved.Plan != nil || saved.Items != nil || saved.NextCursor != "" || saved.PlanID != input.PlanID || !saved.Deleted || saved.Revision <= input.ExpectedRevision || saved.Revision != input.ExpectedRevision+1 {
			return toolError("forbidden", "schedule_result_invalid")
		}
		if err := a.authorizePlanRead(ctx, request, sdk.ScheduleGetToolKey, map[string]string{"plan_id": input.PlanID}); err != nil {
			return err
		}
		reader, ok := a.Plans.(scheduler.ScheduledPlanDeletionReader)
		if !ok {
			return toolError("unavailable", sdk.ResultReadUnsupportedCode)
		}
		current, err := reader.ReadScheduledPlanDeletion(ctx, scheduler.ScheduledPlanLookup{Owner: a.owner(request.Authority), PlanID: input.PlanID})
		if errors.Is(err, scheduler.ErrScheduledPlanDeletionReadUnsupported) {
			return toolError("unavailable", sdk.ResultReadUnsupportedCode)
		}
		if err != nil {
			return err
		}
		if current.PlanID != saved.PlanID || current.Revision != saved.Revision || !current.Deleted {
			return toolError("forbidden", "schedule_result_changed")
		}
		return ctx.Err()
	case sdk.ScheduleCreateToolKey, sdk.ScheduleGetToolKey, sdk.ScheduleUpdateToolKey, sdk.SchedulePauseToolKey, sdk.ScheduleResumeToolKey:
		if saved.Plan == nil || saved.Items != nil || saved.PlanID != "" || saved.Revision != 0 || saved.Deleted || saved.NextCursor != "" {
			return toolError("forbidden", "schedule_result_invalid")
		}
		if request.Definition.Key != sdk.ScheduleCreateToolKey {
			var input struct {
				PlanID           string `json:"plan_id"`
				ExpectedRevision int64  `json:"expected_revision"`
			}
			if json.Unmarshal([]byte(request.Call.Arguments), &input) != nil || input.PlanID != saved.Plan.ID || request.Definition.Key != sdk.ScheduleGetToolKey && (input.ExpectedRevision < 1 || saved.Plan.Revision <= input.ExpectedRevision || saved.Plan.Revision != input.ExpectedRevision+1) {
				return toolError("forbidden", "schedule_result_invalid")
			}
		}
		if request.Definition.Key == sdk.SchedulePauseToolKey && saved.Plan.Status != scheduler.ScheduledPlanStatusPaused || request.Definition.Key == sdk.ScheduleResumeToolKey && saved.Plan.Status != scheduler.ScheduledPlanStatusEnabled {
			return toolError("forbidden", "schedule_result_invalid")
		}
		return a.readSavedPlan(ctx, request, *saved.Plan)
	default:
		return toolError("unavailable", sdk.ResultReadUnsupportedCode)
	}
}

func (a *Adapter) authorizePlanRead(ctx context.Context, original sdk.Request, key string, args any) error {
	var definition sdk.Definition
	for _, candidate := range Definitions() {
		if candidate.Key == key {
			definition = candidate
		}
	}
	body, err := json.Marshal(args)
	if err != nil {
		return err
	}
	// No lease, confirmation, idempotency key or source write action is passed
	// to the current data-reading check. It does not execute the read tool.
	request := sdk.Request{Authority: original.Authority, ConversationID: original.ConversationID, RunID: original.RunID, CorrelationID: original.CorrelationID, Step: original.Step, Definition: definition, Call: sdk.Call{ID: original.Call.ID, Name: key, Arguments: string(body)}}
	auth, err := a.Authorize(ctx, request)
	if err != nil || !auth.Granted || auth.ConfirmationRequired {
		return authErrOrDenied(err)
	}
	return ctx.Err()
}

func (a *Adapter) readSavedPlan(ctx context.Context, request sdk.Request, saved planView) error {
	if !validKey(saved.ID) || saved.Revision < 1 {
		return toolError("forbidden", "schedule_result_invalid")
	}
	if err := a.authorizePlanRead(ctx, request, sdk.ScheduleGetToolKey, map[string]string{"plan_id": saved.ID}); err != nil {
		return err
	}
	current, err := a.Plans.GetScheduledPlan(ctx, scheduler.ScheduledPlanLookup{Owner: a.owner(request.Authority), PlanID: saved.ID})
	if err != nil {
		return err
	}
	view, err := a.view(current)
	if err != nil || current.Owner != a.owner(request.Authority) || current.Status == scheduler.ScheduledPlanStatusDeleted || view.Revision < saved.Revision || view.UpdatedAt.Before(saved.UpdatedAt) {
		return toolError("forbidden", "schedule_result_changed")
	}
	// Status transitions do not rewrite an already delivered receipt. Content,
	// execution target/kind, origin and creation identity must still match; a
	// corrected or deleted source cannot continue exposing its previous text.
	view.Status, view.Revision, view.UpdatedAt = saved.Status, saved.Revision, saved.UpdatedAt
	if digest(view) != digest(saved) {
		return toolError("forbidden", "schedule_result_changed")
	}
	return ctx.Err()
}
