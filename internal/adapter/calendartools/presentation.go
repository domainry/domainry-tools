package calendartools

import (
	"fmt"
	"reflect"
	"unicode/utf8"

	calendar "github.com/domainry/domainry-connector-sdk/calendar"
)

func shortText(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return string([]rune(value)[:limit])
}

type eventView struct {
	calendar.Event
	TextTruncated        bool `json:"text_truncated"`
	DescriptionTruncated bool `json:"description_truncated"`
}

func boundedEvent(event calendar.Event, in arguments) (eventView, error) {
	if event.Validate() != nil || event.CalendarID != in.CalendarID || in.EventID != "" && event.ID != in.EventID || event.SeriesID != "" && !calendar.ValidID(event.SeriesID) {
		return eventView{}, fmt.Errorf("invalid calendar event source")
	}
	for _, moment := range []*calendar.Moment{&event.Start, &event.End, event.OriginalStart} {
		if moment != nil && len(moment.TimeZone) > 128 {
			return eventView{}, fmt.Errorf("invalid calendar source timezone")
		}
	}
	out := eventView{Event: event}
	out.Title = shortText(event.Title, 1024)
	out.Location = shortText(event.Location, 2048)
	descriptionLimit := 1024
	if in.EventID != "" {
		descriptionLimit = 16000
	}
	out.Description = shortText(event.Description, descriptionLimit)
	if len(out.URL) > 2048 {
		out.URL = ""
	}
	out.DescriptionTruncated = out.Description != event.Description
	out.TextTruncated = out.Title != event.Title || out.Location != event.Location || out.URL != event.URL || out.DescriptionTruncated
	return out, nil
}

func present(key string, in arguments, raw []byte) (any, error) {
	switch key {
	case calendar.ListOperationKey:
		var page calendar.CalendarsPage
		if decode(raw, &page) != nil || page.Items == nil || page.Complete != (page.NextCursor == "") || len(page.NextCursor) > 8192 || len(page.Items) > (calendar.PageRequest{Limit: in.Limit}).PageSize() {
			return nil, fmt.Errorf("invalid calendar list source")
		}
		truncated := false
		for i, item := range page.Items {
			if !calendar.ValidID(item.ID) || len(item.TimeZone) > 128 {
				return nil, fmt.Errorf("invalid calendar identity")
			}
			page.Items[i].Name = shortText(item.Name, 256)
			page.Items[i].AccessRole = shortText(item.AccessRole, 64)
			truncated = truncated || page.Items[i] != item
		}
		return struct {
			calendar.CalendarsPage
			TextTruncated bool `json:"text_truncated"`
		}{page, truncated}, nil
	case calendar.EventsOperationKey:
		var page calendar.EventsPage
		if decode(raw, &page) != nil || page.Items == nil || page.Complete != (page.NextCursor == "") || len(page.NextCursor) > 8192 || len(page.Items) > (calendar.PageRequest{Limit: in.Limit}).PageSize() {
			return nil, fmt.Errorf("invalid calendar events source")
		}
		items := []eventView{}
		for _, event := range page.Items {
			view, err := boundedEvent(event, in)
			if err != nil {
				return nil, err
			}
			items = append(items, view)
		}
		return struct {
			Items      []eventView `json:"items"`
			NextCursor string      `json:"next_cursor,omitempty"`
			Complete   bool        `json:"complete"`
			TimeZone   string      `json:"time_zone"`
		}{items, page.NextCursor, page.Complete, in.TimeZone}, nil
	case calendar.EventOperationKey:
		var event calendar.Event
		if decode(raw, &event) != nil {
			return nil, fmt.Errorf("invalid calendar event source")
		}
		return boundedEvent(event, in)
	case calendar.AvailabilityOperationKey:
		var out calendar.Availability
		if decode(raw, &out) != nil {
			return nil, fmt.Errorf("invalid availability source")
		}
		a, b, err := out.Window.Instants()
		start, end, _ := in.Window.Instants()
		if err != nil || !a.Equal(start) || !b.Equal(end) {
			return nil, fmt.Errorf("availability window mismatch")
		}
		verified, err := calendar.ResolveAvailability(calendar.AvailabilityRequest{CalendarIDs: in.CalendarIDs, Window: in.Window, TimeZone: in.TimeZone}, out.Calendars)
		if err != nil || out.Complete != verified.Complete || !reflect.DeepEqual(out.Free, verified.Free) {
			return nil, fmt.Errorf("availability completeness mismatch")
		}
		type status struct {
			CalendarID string   `json:"calendar_id"`
			Complete   bool     `json:"complete"`
			BusyCount  int      `json:"busy_count"`
			ErrorCodes []string `json:"error_codes,omitempty"`
		}
		statuses := []status{}
		for _, c := range verified.Calendars {
			statuses = append(statuses, status{c.CalendarID, c.Complete, len(c.Busy), c.ErrorCodes})
		}
		free, complete, next := verified.Free, verified.Complete, ""
		if len(free) > 50 {
			free = free[:50]
			complete = false
			next = free[len(free)-1].End
		}
		return struct {
			Window          calendar.Window   `json:"window"`
			Calendars       []status          `json:"calendars"`
			Free            []calendar.Window `json:"free"`
			Complete        bool              `json:"complete"`
			NextWindowStart string            `json:"next_window_start,omitempty"`
		}{in.Window, statuses, free, complete, next}, nil
	default:
		return nil, fmt.Errorf("unknown calendar tool")
	}
}
