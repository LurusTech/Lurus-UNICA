package routing

import (
	"context"
	"errors"
	"testing"

	"github.com/kefu/unica/router/internal/state"
)

type fakeCustomers struct {
	byID map[string]*state.Customer
	err  error
}

func (f fakeCustomers) GetCustomer(_ context.Context, id string) (*state.Customer, error) {
	if f.err != nil {
		return nil, f.err
	}
	c, ok := f.byID[id]
	if !ok {
		return nil, errors.New("not found")
	}
	return c, nil
}

// The survey goes out as the conversation closes, and the conversation row
// does not carry the customer's platform user id. Without looking it up every
// survey was refused by the channel adapter and dead-lettered.
func TestSurveyConversationCarriesTheRecipient(t *testing.T) {
	conv := &state.Conversation{ID: "conv-1", ChannelID: "ch-1", ProductLineID: "pl-1", CustomerID: "cust-1"}
	customers := fakeCustomers{byID: map[string]*state.Customer{
		"cust-1": {ID: "cust-1", PlatformIdentity: "openid-abc", ChannelID: "ch-1"},
	}}

	info := surveyConversation(context.Background(), customers, conv)
	if info.CustomerPlatformUserID != "openid-abc" {
		t.Errorf("CustomerPlatformUserID = %q, want openid-abc", info.CustomerPlatformUserID)
	}
	if info.ID != "conv-1" || info.ChannelID != "ch-1" || info.ProductLineID != "pl-1" || info.CustomerID != "cust-1" {
		t.Errorf("conversation fields not carried over: %+v", info)
	}
}

// A failed lookup still produces a survey to send: the gateway dead-letters it
// where it can be traced, rather than the survey vanishing silently here.
func TestSurveyConversationLookupFailureStillSends(t *testing.T) {
	conv := &state.Conversation{ID: "conv-2", ChannelID: "ch-1", ProductLineID: "pl-1", CustomerID: "cust-x"}
	info := surveyConversation(context.Background(), fakeCustomers{err: errors.New("db down")}, conv)
	if info == nil || info.ID != "conv-2" || info.CustomerPlatformUserID != "" {
		t.Errorf("want conversation info without recipient, got %+v", info)
	}
	if info := surveyConversation(context.Background(), nil, conv); info == nil || info.ID != "conv-2" {
		t.Errorf("nil directory: got %+v", info)
	}
}
