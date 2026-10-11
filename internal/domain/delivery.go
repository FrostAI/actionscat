package domain

import (
	"fmt"
	"strings"
)

// DeliveryContext is optional trigger routing, independent of Action source code.
type DeliveryContext struct {
	Platform    string `json:"platform"`
	AdapterID   string `json:"adapter_id,omitempty"`
	MessageType string `json:"message_type"`
	TargetID    string `json:"target_id"`
	SessionID   string `json:"session_id,omitempty"`
	UserID      string `json:"user_id,omitempty"`
	GroupID     string `json:"group_id,omitempty"`
}

func (d *DeliveryContext) Validate() error {
	if d == nil {
		return nil
	}
	for _, value := range []string{d.Platform, d.AdapterID, d.MessageType, d.TargetID, d.SessionID, d.UserID, d.GroupID} {
		if len(value) > 4096 || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("invalid delivery context field")
		}
	}
	if d.Platform == "" || d.TargetID == "" {
		return fmt.Errorf("delivery context requires platform and target_id")
	}
	switch d.MessageType {
	case "private":
		if d.GroupID != "" || (d.UserID != "" && d.UserID != d.TargetID) {
			return fmt.Errorf("private delivery context has inconsistent user/group target")
		}
	case "group":
		if d.GroupID != "" && d.GroupID != d.TargetID {
			return fmt.Errorf("group delivery context has inconsistent group target")
		}
	default:
		return fmt.Errorf("delivery context message_type must be private or group")
	}
	return nil
}

func (d *DeliveryContext) Environment() map[string]string {
	if d == nil {
		return nil
	}
	return map[string]string{
		"ACTIONSCAT_EVENT_PLATFORM":     d.Platform,
		"ACTIONSCAT_EVENT_ADAPTER_ID":   d.AdapterID,
		"ACTIONSCAT_EVENT_MESSAGE_TYPE": d.MessageType,
		"ACTIONSCAT_EVENT_TARGET_ID":    d.TargetID,
		"ACTIONSCAT_EVENT_SESSION_ID":   d.SessionID,
		"ACTIONSCAT_EVENT_USER_ID":      d.UserID,
		"ACTIONSCAT_EVENT_GROUP_ID":     d.GroupID,
	}
}
