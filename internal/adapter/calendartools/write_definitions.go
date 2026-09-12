package calendartools

import (
	"encoding/json"
	"github.com/domainry/domainry-connector-sdk/calendarwrite"
	sdk "github.com/domainry/domainry-tools-sdk"
)

const WriteAccountsKey = "calendar_write_accounts"

func writeObject(fields map[string]any, required ...string) any {
	return map[string]any{"type": "object", "properties": fields, "required": append([]string{}, required...), "additionalProperties": false}
}
func writeText(max int, nonempty bool) any {
	out := map[string]any{"type": "string", "maxLength": max}
	if nonempty {
		out["minLength"] = 1
	}
	return out
}
func writeDefinition(key, action, description, effect string, input any) sdk.Definition {
	raw, _ := json.Marshal(input)
	idem := "natural"
	if effect == "write" {
		idem = "reconcile"
	}
	return sdk.Definition{Key: key, Version: "1", ActionKey: "calendar." + action, Description: description, InputSchema: raw, OutputSchema: json.RawMessage(`{"type":"object"}`), Effect: effect, Idempotency: idem, TimeoutMillis: 90000, MaxOutputBytes: 1048576}
}

func inspectDefinition() sdk.Definition {
	return writeDefinition(calendarwrite.InspectOperationKey, "event_inspect", "Inspect the exact event before editing: complete target attendees, actual ETag, event kind, recurrence target and original content. Never substitute a series ID for an occurrence. This is a read, not approval; copy the returned version into the update.", "read", writeObject(map[string]any{"account_key": writeText(1024, true), "calendar_id": writeText(2048, true), "event_id": writeText(2048, true), "time_zone": writeText(128, true)}, "account_key", "calendar_id", "event_id", "time_zone"))
}

func WriteDefinitions() []sdk.Definition { return append(writeDefinitions(), inspectDefinition()) }

func writeDefinitions() []sdk.Definition {
	moment := map[string]any{"oneOf": []any{
		writeObject(map[string]any{"date": map[string]any{"type": "string", "format": "date"}, "time_zone": writeText(128, true)}, "date", "time_zone"),
		writeObject(map[string]any{"date_time": map[string]any{"type": "string", "format": "date-time"}, "time_zone": writeText(128, true)}, "date_time", "time_zone"),
	}}
	attendees := map[string]any{"type": "array", "maxItems": 100, "items": writeObject(map[string]any{"address": writeText(512, true), "name": writeText(512, false), "kind": map[string]any{"type": "string", "enum": []string{"required", "optional", "resource"}}}, "address", "kind")}
	event := writeObject(map[string]any{"title": writeText(1024, true), "description": writeText(65536, false), "location": writeText(4096, false), "start": moment, "end": moment, "attendees": attendees}, "title", "start", "end", "attendees")
	notifications := map[string]any{"type": "string", "const": calendarwrite.NotifyAttendees}
	create := writeObject(map[string]any{"calendar_id": writeText(2048, true), "event": event, "notifications": notifications}, "calendar_id", "event", "notifications")
	patch := writeObject(map[string]any{"title": writeText(1024, true), "description": writeText(65536, false), "location": writeText(4096, false), "start": moment, "end": moment, "attendees": attendees}).(map[string]any)
	patch["minProperties"] = 1
	patch["dependentRequired"] = map[string]any{"start": []string{"end"}, "end": []string{"start"}}
	update := writeObject(map[string]any{"calendar_id": writeText(2048, true), "event_id": writeText(2048, true), "expected_version": writeText(1024, true), "scope": map[string]any{"type": "string", "enum": []string{calendarwrite.ScopeEvent, calendarwrite.ScopeSeries}}, "changes": patch, "notifications": notifications}, "calendar_id", "event_id", "expected_version", "scope", "changes", "notifications")
	envelope := func(request any) any {
		return writeObject(map[string]any{"account_key": writeText(1024, true), "account_updated_at": writeText(128, true), "request": request}, "account_key", "account_updated_at", "request")
	}
	return []sdk.Definition{
		writeDefinition(WriteAccountsKey, "write_accounts", "Discover current accounts authorized for a calendar write. Copy key and account_updated_at exactly into the write. Follow next_cursor; discovery grants no approval to execute.", "read", writeObject(map[string]any{"operation": map[string]any{"type": "string", "enum": []string{calendarwrite.CreateOperationKey, calendarwrite.UpdateOperationKey}}, "cursor": writeText(512, true), "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10}})),
		writeDefinition(calendarwrite.CreateOperationKey, "event_create", "Create one event after exact-content approval. Explicit attendees and notify_attendees are required. Use IANA zones and matching offsets; all-day end dates are exclusive. Do not invent account revisions or delivery claims. Uncertain results require original receipt lookup, never another send.", "write", envelope(create)),
		writeDefinition(calendarwrite.UpdateOperationKey, "event_update", "Patch the inspected event using its exact ETag and account revision. Omit untouched fields; empty description/location/attendees clears them. A series edit requires explicit series scope and can also notify separately changed occurrences. Changes notify attendees; their delivery is not guaranteed.", "write", envelope(update)),
	}
}
