package capability

import (
	"errors"
	"testing"
)

func TestStaticAndRuntimeStatuses(t *testing.T) {
	report := Static(ChannelsList, MessagesHistory)
	if report.Capabilities[0].Status != Available || report.Capabilities[2].Status != Unsupported || report.Capabilities[2].Reason != ReasonAdapterNotImplemented {
		t.Fatalf("static report = %+v", report.Capabilities)
	}
	report = Apply(report, map[ID]Result{ChannelsList: {Status: Restricted, Reason: ReasonAPIKeyRestricted}, MessagesHistory: {Status: Unknown}})
	if report.Capabilities[0].Status != Restricted || report.Capabilities[0].Reason != ReasonAPIKeyRestricted || report.Capabilities[1].Status != Unknown {
		t.Fatalf("runtime report = %+v", report.Capabilities)
	}
}

func TestMessagesEditIsWiredIn(t *testing.T) {
	found := false
	for _, id := range IDs {
		if id == MessagesEdit {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("MessagesEdit missing from IDs = %+v", IDs)
	}
	if Label(MessagesEdit) == "" {
		t.Fatal("Label(MessagesEdit) is empty")
	}
	if Action(MessagesEdit) != "messages.edit" {
		t.Fatalf("Action(MessagesEdit) = %q, want %q", Action(MessagesEdit), "messages.edit")
	}
}

func TestUnavailableErrorIsTyped(t *testing.T) {
	err := &UnavailableError{ID: MessagesSend, Status: Restricted, Reason: ReasonAPIKeyRestricted}
	var target *UnavailableError
	if !errors.As(err, &target) || target.ID != MessagesSend {
		t.Fatalf("error = %v", err)
	}
}
