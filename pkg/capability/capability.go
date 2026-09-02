// Package capability defines the protocol-independent capability contract
// shared by chat adapters and the engine core.
package capability

import "fmt"

type ID string

const (
	ChannelsList              ID = "channels.list"
	MessagesHistory           ID = "messages.history"
	MessagesSearch            ID = "messages.search"
	MessagesSend              ID = "messages.send"
	MessagesReact             ID = "messages.react"
	MessagesEdit              ID = "messages.edit"
	MessagesDelete            ID = "messages.delete"
	MessagesDeleteRange       ID = "messages.delete_range"
	ChannelsWatch             ID = "channels.watch"
	NotificationsCheck        ID = "notifications.check"
	NotificationsCheckCursors ID = "notifications.check_cursors"
)

var IDs = []ID{ChannelsList, MessagesHistory, MessagesSearch, MessagesSend, MessagesReact, MessagesEdit, MessagesDelete, MessagesDeleteRange, ChannelsWatch, NotificationsCheck, NotificationsCheckCursors}

type Status string

const (
	Available   Status = "available"
	Unsupported Status = "unsupported"
	Restricted  Status = "restricted"
	Unknown     Status = "unknown"
)

type Reason string

const (
	ReasonAdapterNotImplemented Reason = "adapter_not_implemented"
	ReasonAPIKeyRestricted      Reason = "api_key_restricted"
	ReasonAPIUnsupported        Reason = "api_unsupported"
)

type Capability struct {
	ID     ID     `json:"id"`
	Label  string `json:"label"`
	Action string `json:"action"`
	Status Status `json:"status"`
	Reason Reason `json:"reason,omitempty"`
}

type Report struct {
	Capabilities []Capability `json:"capabilities"`
}

type Result struct {
	Status Status
	Reason Reason
}

type CapabilityProvider interface {
	CapabilityReport(refresh bool) (Report, error)
}

type UnavailableError struct {
	ID     ID
	Status Status
	Reason Reason
}

func (e *UnavailableError) Error() string { return fmt.Sprintf("capability %s is %s", e.ID, e.Status) }

func Label(id ID) string {
	return map[ID]string{
		ChannelsList: "List channels", MessagesHistory: "Read message history", MessagesSearch: "Search messages",
		MessagesSend: "Send messages", MessagesReact: "React to messages", MessagesEdit: "Edit messages", MessagesDelete: "Delete messages",
		MessagesDeleteRange: "Delete message ranges", ChannelsWatch: "Watch channels", NotificationsCheck: "Check notifications", NotificationsCheckCursors: "Check notifications with cursors",
	}[id]
}

func Action(id ID) string {
	return map[ID]string{
		ChannelsList: "channels.list", MessagesHistory: "messages.history", MessagesSearch: "messages.search",
		MessagesSend: "messages.send", MessagesReact: "messages.react", MessagesEdit: "messages.edit", MessagesDelete: "messages.delete",
		MessagesDeleteRange: "messages.delete_range", ChannelsWatch: "channels.watch", NotificationsCheck: "notifications.check", NotificationsCheckCursors: "notifications.check_cursors",
	}[id]
}

func Static(supported ...ID) Report {
	set := make(map[ID]bool, len(supported))
	for _, id := range supported {
		set[id] = true
	}
	report := Report{Capabilities: make([]Capability, 0, len(IDs))}
	for _, id := range IDs {
		c := Capability{ID: id, Label: Label(id), Action: Action(id), Status: Unsupported, Reason: ReasonAdapterNotImplemented}
		if set[id] {
			c.Status, c.Reason = Available, ""
		}
		report.Capabilities = append(report.Capabilities, c)
	}
	return report
}

func Apply(report Report, results map[ID]Result) Report {
	for i := range report.Capabilities {
		if result, ok := results[report.Capabilities[i].ID]; ok {
			report.Capabilities[i].Status, report.Capabilities[i].Reason = result.Status, result.Reason
		}
	}
	return report
}

func AllUnknown() Report {
	results := make(map[ID]Result, len(IDs))
	for _, id := range IDs {
		results[id] = Result{Status: Unknown}
	}
	return Apply(Static(IDs...), results)
}
