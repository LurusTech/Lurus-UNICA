package state

import "fmt"

// ConversationState represents the current state of a conversation in the system.
type ConversationState string

const (
	StatePending         ConversationState = "pending"
	StateAIProcessing    ConversationState = "ai_processing"
	StateHumanProcessing ConversationState = "human_processing"
	StateClosed          ConversationState = "closed"
)

// validTransitions defines the allowed state transitions for a conversation.
var validTransitions = map[ConversationState][]ConversationState{
	StatePending:         {StateAIProcessing, StateHumanProcessing},
	StateAIProcessing:    {StateHumanProcessing, StateClosed},
	StateHumanProcessing: {StateClosed},
	StateClosed:          {StateAIProcessing},
}

// allStates lists every valid conversation state for validation purposes.
var allStates = map[ConversationState]bool{
	StatePending:         true,
	StateAIProcessing:    true,
	StateHumanProcessing: true,
	StateClosed:          true,
}

// IsValidState checks whether a given state string is a recognized conversation state.
func IsValidState(s ConversationState) bool {
	return allStates[s]
}

// CanTransitionTo checks whether transitioning from the current state to the target state is allowed.
func (s ConversationState) CanTransitionTo(target ConversationState) bool {
	targets, ok := validTransitions[s]
	if !ok {
		return false
	}
	for _, t := range targets {
		if t == target {
			return true
		}
	}
	return false
}

// ErrInvalidTransition is returned when a state transition is not permitted.
type ErrInvalidTransition struct {
	From ConversationState
	To   ConversationState
}

func (e *ErrInvalidTransition) Error() string {
	return fmt.Sprintf("invalid state transition from %q to %q", e.From, e.To)
}

// ValidateTransition returns nil if the transition is valid, or an error describing the violation.
func ValidateTransition(from, to ConversationState) error {
	if !IsValidState(from) {
		return fmt.Errorf("unknown source state: %q", from)
	}
	if !IsValidState(to) {
		return fmt.Errorf("unknown target state: %q", to)
	}
	if !from.CanTransitionTo(to) {
		return &ErrInvalidTransition{From: from, To: to}
	}
	return nil
}
