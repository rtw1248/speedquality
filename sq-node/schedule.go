package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type availabilityWindow struct {
	startMinute int
	endMinute   int
}

func parseClockMinute(value string) (int, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, errors.New("时间必须使用 HH:MM 格式")
	}
	hour, hourErr := strconv.Atoi(parts[0])
	minute, minuteErr := strconv.Atoi(parts[1])
	if hourErr != nil || minuteErr != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, errors.New("时间必须使用有效的 24 小时制 HH:MM")
	}
	return hour*60 + minute, nil
}

func parseAvailability(value string) ([]availabilityWindow, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "always" {
		return nil, nil
	}
	if value == "" {
		return nil, errors.New("可用时间不能为空")
	}
	parts := strings.Split(value, ",")
	if len(parts) > 8 {
		return nil, errors.New("可用时间段最多 8 个")
	}
	windows := make([]availabilityWindow, 0, len(parts))
	for _, part := range parts {
		bounds := strings.Split(strings.TrimSpace(part), "-")
		if len(bounds) != 2 {
			return nil, errors.New("可用时间应为 always 或 HH:MM-HH:MM")
		}
		start, err := parseClockMinute(bounds[0])
		if err != nil {
			return nil, err
		}
		end, err := parseClockMinute(bounds[1])
		if err != nil {
			return nil, err
		}
		if start == end {
			return nil, errors.New("起止时间相同时请使用 always")
		}
		windows = append(windows, availabilityWindow{startMinute: start, endMinute: end})
	}
	return windows, nil
}

func configLocation(value string) (*time.Location, error) {
	value = strings.TrimSpace(value)
	if value == "Local" {
		return time.Local, nil
	}
	if value == "UTC" {
		return time.UTC, nil
	}
	if value == "" || len(value) > 64 || strings.ContainsAny(value, "\r\n\x00") {
		return nil, errors.New("时区无效")
	}
	location, err := time.LoadLocation(value)
	if err != nil {
		return nil, fmt.Errorf("无法加载时区 %s", value)
	}
	return location, nil
}

func availableAt(config Config, now time.Time) bool {
	windows, err := parseAvailability(config.Availability)
	if err != nil {
		return false
	}
	if len(windows) == 0 {
		return true
	}
	location, err := configLocation(config.Timezone)
	if err != nil {
		return false
	}
	local := now.In(location)
	minute := local.Hour()*60 + local.Minute()
	for _, window := range windows {
		if window.startMinute < window.endMinute {
			if minute >= window.startMinute && minute < window.endMinute {
				return true
			}
			continue
		}
		if minute >= window.startMinute || minute < window.endMinute {
			return true
		}
	}
	return false
}
