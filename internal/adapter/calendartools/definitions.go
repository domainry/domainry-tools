package calendartools

import (
	"encoding/json"
	calendar "github.com/domainry/domainry-connector-sdk/calendar"
	sdk "github.com/domainry/domainry-tools-sdk"
)

const AccountsKey = "calendar_accounts"

func Definitions() []sdk.Definition {
	str := func(max int) any { return map[string]any{"type": "string", "minLength": 1, "maxLength": max} }
	object := func(fields map[string]any, required ...string) any {
		return map[string]any{"type": "object", "properties": fields, "required": append([]string{}, required...), "additionalProperties": false}
	}
	window := object(map[string]any{"start": str(64), "end": str(64)}, "start", "end")
	page := map[string]any{"type": "integer", "minimum": 1, "maximum": 25}
	definition := func(key, action, description string, input any) sdk.Definition {
		raw, _ := json.Marshal(input)
		return sdk.Definition{Key: key, Version: "1", ActionKey: "calendar." + action, Description: description, InputSchema: raw, OutputSchema: json.RawMessage(`{"type":"object"}`), Effect: "read", Idempotency: "natural", Parallelism: sdk.ToolParallelismIndependentRead, TimeoutMillis: 90000, MaxOutputBytes: 1048576}
	}
	return []sdk.Definition{
		definition(AccountsKey, "accounts", "Discover current-user calendar accounts authorized for a chosen read operation. Follow next_cursor while complete=false. Account discovery grants no access to other operations.", object(map[string]any{"operation": map[string]any{"type": "string", "enum": []string{calendar.ListOperationKey, calendar.EventsOperationKey, calendar.EventOperationKey, calendar.AvailabilityOperationKey}}, "cursor": str(512), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10}})),
		definition(calendar.ListOperationKey, "list", "List calendars on one authorized account. Use returned calendar IDs for subsequent reads. Follow next_cursor; partial pages are not the full calendar list.", object(map[string]any{"account_key": str(1024), "cursor": str(8192), "limit": page}, "account_key")),
		definition(calendar.EventsOperationKey, "events", "Read expanded events in an absolute half-open time window. Provide RFC3339 start/end with offsets and an IANA time_zone. All-day dates and exclusive end dates must remain dates. Follow next_cursor; text truncation is explicit.", object(map[string]any{"account_key": str(1024), "calendar_id": str(1024), "window": window, "time_zone": str(128), "cursor": str(8192), "limit": page}, "account_key", "calendar_id", "window", "time_zone")),
		definition(calendar.EventOperationKey, "event", "Read one event on its authorized account/calendar. Preserve all-day dates and instant offsets. description_truncated and text_truncated explicitly identify shortened content.", object(map[string]any{"account_key": str(1024), "calendar_id": str(1024), "event_id": str(1024), "time_zone": str(128)}, "account_key", "calendar_id", "event_id", "time_zone")),
		definition(calendar.AvailabilityOperationKey, "availability", "Find common free intervals across selected calendar IDs in one account. Unknown or partial upstream results never imply free time. At most 50 free intervals are returned; follow next_window_start when supplied. Calendar statuses and busy counts accompany the computed intervals.", object(map[string]any{"account_key": str(1024), "calendar_ids": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "uniqueItems": true, "items": str(1024)}, "window": window, "time_zone": str(128)}, "account_key", "calendar_ids", "window", "time_zone")),
	}
}
