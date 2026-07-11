package state

import (
	"testing"
)

func TestIsValidState(t *testing.T) {
	tests := []struct {
		state ConversationState
		valid bool
	}{
		{StatePending, true},
		{StateAIProcessing, true},
		{StateHumanProcessing, true},
		{StateClosed, true},
		{ConversationState("unknown"), false},
		{ConversationState(""), false},
	}
	for _, tt := range tests {
		if got := IsValidState(tt.state); got != tt.valid {
			t.Errorf("IsValidState(%q) = %v, want %v", tt.state, got, tt.valid)
		}
	}
}

func TestCanTransitionTo_ValidTransitions(t *testing.T) {
	validCases := []struct {
		from ConversationState
		to   ConversationState
	}{
		{StatePending, StateAIProcessing},
		{StatePending, StateHumanProcessing},
		{StateAIProcessing, StateHumanProcessing},
		{StateAIProcessing, StateClosed},
		{StateHumanProcessing, StateClosed},
		{StateClosed, StateAIProcessing},
	}
	for _, tt := range validCases {
		if !tt.from.CanTransitionTo(tt.to) {
			t.Errorf("expected transition %s -> %s to be valid", tt.from, tt.to)
		}
	}
}

func TestCanTransitionTo_InvalidTransitions(t *testing.T) {
	invalidCases := []struct {
		from ConversationState
		to   ConversationState
	}{
		// Pending cannot go to closed directly
		{StatePending, StateClosed},
		// AI processing cannot go back to pending
		{StateAIProcessing, StatePending},
		// Human processing cannot go to pending or AI
		{StateHumanProcessing, StatePending},
		{StateHumanProcessing, StateAIProcessing},
		// Closed can only go to AI processing
		{StateClosed, StatePending},
		{StateClosed, StateHumanProcessing},
		{StateClosed, StateClosed},
		// Self-transitions should be invalid
		{StatePending, StatePending},
		{StateAIProcessing, StateAIProcessing},
		{StateHumanProcessing, StateHumanProcessing},
	}
	for _, tt := range invalidCases {
		if tt.from.CanTransitionTo(tt.to) {
			t.Errorf("expected transition %s -> %s to be invalid", tt.from, tt.to)
		}
	}
}

func TestValidateTransition_Valid(t *testing.T) {
	err := ValidateTransition(StatePending, StateAIProcessing)
	if err != nil {
		t.Errorf("expected valid transition, got error: %v", err)
	}
}

func TestValidateTransition_InvalidTransition(t *testing.T) {
	err := ValidateTransition(StatePending, StateClosed)
	if err == nil {
		t.Error("expected error for invalid transition")
	}
	if _, ok := err.(*ErrInvalidTransition); !ok {
		t.Errorf("expected ErrInvalidTransition, got %T: %v", err, err)
	}
}

func TestValidateTransition_UnknownState(t *testing.T) {
	err := ValidateTransition(ConversationState("bogus"), StateAIProcessing)
	if err == nil {
		t.Error("expected error for unknown source state")
	}

	err = ValidateTransition(StatePending, ConversationState("bogus"))
	if err == nil {
		t.Error("expected error for unknown target state")
	}
}

func TestErrInvalidTransition_Error(t *testing.T) {
	e := &ErrInvalidTransition{From: StatePending, To: StateClosed}
	expected := `invalid state transition from "pending" to "closed"`
	if e.Error() != expected {
		t.Errorf("error message = %q, want %q", e.Error(), expected)
	}
}

func TestCanTransitionTo_UnknownSourceState(t *testing.T) {
	unknown := ConversationState("nonexistent")
	if unknown.CanTransitionTo(StatePending) {
		t.Error("unknown state should not be able to transition to anything")
	}
}

func TestAllValidTransitionsAreExhaustive(t *testing.T) {
	// Verify that every state has an entry in validTransitions
	states := []ConversationState{StatePending, StateAIProcessing, StateHumanProcessing, StateClosed}
	for _, s := range states {
		if _, ok := validTransitions[s]; !ok {
			t.Errorf("state %q missing from validTransitions map", s)
		}
	}
}

func TestReopenClosedConversation(t *testing.T) {
	// Closed conversations can be reopened by transitioning to AI processing
	if !StateClosed.CanTransitionTo(StateAIProcessing) {
		t.Error("closed conversations should be reopenable to ai_processing")
	}
}
