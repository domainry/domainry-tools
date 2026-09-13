package scheduletools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	scheduler "github.com/domainry/domainry-scheduler-sdk"
	sdk "github.com/domainry/domainry-tools-sdk"
	tools "github.com/domainry/domainry-tools/internal/application/tool"
)

type receiptPlanService struct{ planServiceStub }

func (s *receiptPlanService) ReadScheduledPlanDeletion(ctx context.Context, lookup scheduler.ScheduledPlanLookup) (scheduler.ScheduledPlanDeleteReceipt, error) {
	if err := ctx.Err(); err != nil {
		return scheduler.ScheduledPlanDeleteReceipt{}, err
	}
	plan, ok := s.plans[lookup.PlanID]
	if !ok || plan.Owner != lookup.Owner || plan.Status != scheduler.ScheduledPlanStatusDeleted {
		return scheduler.ScheduledPlanDeleteReceipt{}, scheduler.ErrScheduledPlanNotFound
	}
	return scheduler.ScheduledPlanDeleteReceipt{PlanID: plan.ID, Revision: plan.Revision, Deleted: true}, nil
}

func TestScheduleReceiptReadUsesCurrentDataGrantWithoutMutationOrCatalog(t *testing.T) {
	for _, key := range []string{sdk.ScheduleCreateToolKey, sdk.ScheduleListToolKey, sdk.ScheduleGetToolKey, sdk.ScheduleUpdateToolKey, sdk.SchedulePauseToolKey, sdk.ScheduleResumeToolKey, sdk.ScheduleDeleteToolKey} {
		t.Run(key, func(t *testing.T) {
			plans := &receiptPlanService{}
			canWrite, canRead, checks := true, true, 0
			authority := sdk.Authority{Known: true, RuntimeID: "runtime", WorkspaceID: "workspace", UserID: "reader"}
			adapter := &Adapter{Plans: plans, ProductKey: "agent", Catalog: catalogStub{{Key: "todo_list", ActionKey: "agent.conversation_tools.todo_list"}}, Authorize: func(_ context.Context, request sdk.Request) (sdk.Authorization, error) {
				checks++
				if !canWrite {
					if request.Definition.Key != sdk.ScheduleGetToolKey && request.Definition.Key != sdk.ScheduleListToolKey {
						t.Error("read requested mutation permission", request.Definition.Key)
					}
					if request.IdempotencyKey != "" || request.LeaseOwner != "" || request.Fence != 0 || request.Confirmation != nil {
						t.Error("read acquired execution credentials")
					}
				}
				granted := canWrite
				if request.Definition.Effect == "read" {
					granted = canRead
				}
				return sdk.Authorization{Granted: granted, UserTimezone: "Asia/Shanghai"}, nil
			}}
			registry := tools.NewRegistry()
			if err := adapter.Register(registry); err != nil {
				t.Fatal(err)
			}
			keys := []string{}
			for _, d := range Definitions() {
				keys = append(keys, d.Key)
			}
			host, err := registry.Select(keys)
			if err != nil {
				t.Fatal(err)
			}
			var request sdk.Request
			invoke := func(key, args string) sdk.Result {
				var def sdk.Definition
				for _, d := range Definitions() {
					if d.Key == key {
						def = d
					}
				}
				request = sdk.Request{Authority: authority, ConversationID: "conversation", RunID: "run", Definition: def, Call: sdk.Call{ID: "call", Name: key, Arguments: args}, IdempotencyKey: "create-key"}
				result, err := host.InvokeConversationTool(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				return result
			}
			result := invoke(sdk.ScheduleCreateToolKey, `{"kind":"background_task","name":"原始计划","trigger":{"type":"recurring","schedule":{"type":"daily_at","time_of_day":"09:00"}},"details":{"goal":"整理待办","allowed_tools":["todo_list"]}}`)
			id := result.ResourceID
			switch key {
			case sdk.ScheduleListToolKey:
				result = invoke(key, `{}`)
			case sdk.ScheduleGetToolKey:
				result = invoke(key, fmt.Sprintf(`{"plan_id":%q}`, id))
			case sdk.ScheduleUpdateToolKey:
				result = invoke(key, fmt.Sprintf(`{"plan_id":%q,"expected_revision":1,"name":"更新后计划"}`, id))
			case sdk.SchedulePauseToolKey, sdk.ScheduleDeleteToolKey:
				result = invoke(key, fmt.Sprintf(`{"plan_id":%q,"expected_revision":1}`, id))
			case sdk.ScheduleResumeToolKey:
				invoke(sdk.SchedulePauseToolKey, fmt.Sprintf(`{"plan_id":%q,"expected_revision":1}`, id))
				result = invoke(key, fmt.Sprintf(`{"plan_id":%q,"expected_revision":2}`, id))
			}
			originalBody := string(result.Content)
			canWrite = false
			adapter.Catalog = nil
			read := func() error { return host.AuthorizeConversationToolResultRead(t.Context(), request, result) }
			before := digest(plans.plans)
			if err := read(); err != nil {
				t.Fatal("original receipt needs mutation/catalog", err)
			}
			if digest(plans.plans) != before || string(result.Content) != originalBody {
				t.Fatal("read changed plan or receipt")
			}
			canRead = false
			if err := read(); err == nil {
				t.Fatal("revoked plan read disclosed receipt")
			}
			canRead = true
			request.Authority.UserID = "other"
			if err := read(); err == nil {
				t.Fatal("foreign owner received receipt")
			}
			request.Authority = authority
			var saved map[string]any
			json.Unmarshal(result.Content, &saved)
			saved["operation"] = "fake"
			result.Content, _ = json.Marshal(saved)
			if err := read(); err == nil {
				t.Fatal("forged receipt accepted")
			}
			result.Content = json.RawMessage(originalBody)
			if key == sdk.ScheduleDeleteToolKey {
				adapter.Plans = &plans.planServiceStub
				var coded *sdk.Error
				if err := read(); !errors.As(err, &coded) || coded.Code != sdk.ResultReadUnsupportedCode {
					t.Fatal("missing tombstone owner did not use explicit unsupported", err)
				}
				adapter.Plans = plans
			} else {
				plan := plans.plans[id]
				plan.Status = scheduler.ScheduledPlanStatusPaused
				plan.Revision++
				plans.plans[id] = plan
				if err := read(); err != nil || string(result.Content) != originalBody {
					t.Fatal("later status replaced/rejected original receipt", err)
				}
				plan.Name = "corrected private content"
				plans.plans[id] = plan
				if err := read(); err == nil {
					t.Fatal("corrected source retained stale text")
				}
			}
			delete(plans.plans, id)
			if err := read(); err == nil {
				t.Fatal("missing plan treated as authoritative receipt")
			}
			before = digest(plans.plans)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if err := host.AuthorizeConversationToolResultRead(ctx, request, result); err == nil {
				t.Fatal("canceled read succeeded")
			}
			if digest(plans.plans) != before || checks == 0 {
				t.Fatal("unexpected read effects")
			}
		})
	}
}
