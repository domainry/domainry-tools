// Package timeutil implements the deterministic trusted-clock tool.
package timeutil

import (
	"fmt"
	"time"
)

type Input struct {
	Timezone string `json:"timezone"`
	Relative string `json:"relative_date"`
}

func Resolve(in Input, now time.Time, fallback *time.Location, userTimezone string) (map[string]any, error) {
	location := fallback
	if location == nil {
		location = time.UTC
	}
	source := "host_default"
	var err error
	if userTimezone != "" {
		location, err = time.LoadLocation(userTimezone)
		if err != nil {
			return nil, err
		}
		source = "user_profile"
	}
	if in.Timezone != "" {
		location, err = time.LoadLocation(in.Timezone)
		if err != nil {
			return nil, err
		}
		source = "requested"
	}
	now = now.In(location)
	out := map[string]any{"now": now.Format(time.RFC3339Nano), "timezone": location.String(), "timezone_source": source, "date": now.Format("2006-01-02"), "weekday": now.Weekday().String()}
	if in.Relative != "" {
		days := 0
		switch in.Relative {
		case "today":
		case "tomorrow":
			days = 1
		case "yesterday":
			days = -1
		case "next_week":
			days = 8 - int(now.Weekday())
			if now.Weekday() == time.Sunday {
				days = 1
			}
		default:
			return nil, fmt.Errorf("invalid relative date")
		}
		out["relative_date"] = in.Relative
		out["resolved_date"] = now.AddDate(0, 0, days).Format("2006-01-02")
	}
	return out, nil
}
