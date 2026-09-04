package capability

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kefu/unica/admin/internal/bridge"
)

// stubRouter stands in for the router bridge. The interesting deployments for
// this package are the broken ones, and a stub is the only way to hold the
// router in a state that is neither working nor absent.
type stubRouter struct {
	switches *bridge.RuntimeSwitches
	err      error
	calls    int
}

func (s *stubRouter) Switches(ctx context.Context) (*bridge.RuntimeSwitches, error) {
	s.calls++
	return s.switches, s.err
}

func byKey(t *testing.T, list []Capability, key string) Capability {
	t.Helper()
	for _, c := range list {
		if c.Key == key {
			return c
		}
	}
	t.Fatalf("capability %q missing from %+v", key, list)
	return Capability{}
}

func workingRouter() *stubRouter {
	return &stubRouter{switches: &bridge.RuntimeSwitches{
		IntentTriage:    "shadow",
		SceneMode:       "on",
		OntologyEnabled: true,
		ACESTEnabled:    true,
	}}
}

// An empty dataset API key is the original silent failure this package exists
// for, so the test insists on more than the state: a bare "off" with no reason
// leaves the tenant exactly where they were.
func TestKnowledgeManagementIsOffWithAReasonWhenTheDatasetKeyIsEmpty(t *testing.T) {
	list := NewProbe("", "http://router:8081", workingRouter()).List(context.Background())

	km := byKey(t, list, "knowledge_management")
	if km.State != StateOff {
		t.Fatalf("state = %q, want %q", km.State, StateOff)
	}
	if km.Reason == "" {
		t.Fatal("an off capability with no reason tells the reader nothing they can act on")
	}
	if km.Owner != OwnerAdmin {
		t.Errorf("owner = %q, want %q", km.Owner, OwnerAdmin)
	}

	// The same probe with a key set must not keep explaining itself.
	list = NewProbe("dataset-key", "http://router:8081", workingRouter()).List(context.Background())
	if km = byKey(t, list, "knowledge_management"); km.State != StateOn || km.Reason != "" {
		t.Fatalf("with a key configured: state = %q reason = %q, want %q and no reason", km.State, km.Reason, StateOn)
	}
}

// The failure this whole increment is against: a source that could not be
// reached rendered as a negative answer. "off" here would send an operator
// looking for a switch that may already be in the position they want.
func TestUnreachableRouterMakesACESTUnknownRatherThanOff(t *testing.T) {
	router := &stubRouter{err: errors.New("connection refused")}

	list := NewProbe("dataset-key", "http://router:8081", router).List(context.Background())

	acest := byKey(t, list, "acest")
	if acest.State == StateOff {
		t.Fatal("acest reported off on the strength of a failed read; the router was never asked successfully")
	}
	if acest.State != StateUnknown {
		t.Fatalf("state = %q, want %q", acest.State, StateUnknown)
	}
	if acest.Reason == "" {
		t.Fatal("unknown without a reason is indistinguishable from a bug")
	}
	if acest.Owner != OwnerRouter {
		t.Errorf("owner = %q, want %q", acest.Owner, OwnerRouter)
	}

	// router_runtime reflects whether an address was configured at all: one is
	// here, so this is not the "not enabled in this deployment" case, however
	// badly the read went.
	rt := byKey(t, list, "router_runtime")
	if rt.State == StateOff {
		t.Fatal("router_runtime off although ROUTER_INTERNAL_URL is set: that reads as a deployment that never had a router")
	}
	if rt.Reason == "" {
		t.Fatal("a router that is configured but unreachable needs to say so")
	}

	if router.calls != 1 {
		t.Errorf("router asked %d times, want 1: two entries share one answer", router.calls)
	}
}

// A bridge that returns neither switches nor an error would otherwise be read
// as "everything the router owns is off".
func TestEmptyRouterAnswerIsUnknownNotOff(t *testing.T) {
	list := NewProbe("dataset-key", "http://router:8081", &stubRouter{}).List(context.Background())

	if acest := byKey(t, list, "acest"); acest.State != StateUnknown {
		t.Fatalf("acest state = %q, want %q", acest.State, StateUnknown)
	}
	if rt := byKey(t, list, "router_runtime"); rt.State != StateUnknown {
		t.Fatalf("router_runtime state = %q, want %q", rt.State, StateUnknown)
	}
}

// No address at all is the one case where the router-owned answer really is
// disabled in this deployment — but even then acest is unknown, not off,
// because nothing was ever able to look at it.
func TestNoRouterAddressLeavesRouterRuntimeOffAndACESTUnknown(t *testing.T) {
	list := NewProbe("dataset-key", "  ", workingRouter()).List(context.Background())

	rt := byKey(t, list, "router_runtime")
	if rt.State != StateOff {
		t.Fatalf("router_runtime state = %q, want %q", rt.State, StateOff)
	}
	if rt.Reason == "" {
		t.Fatal("router_runtime off without a reason")
	}

	acest := byKey(t, list, "acest")
	if acest.State != StateUnknown {
		t.Fatalf("acest state = %q, want %q — an unasked question has no answer", acest.State, StateUnknown)
	}
	if acest.Reason == "" {
		t.Fatal("acest unknown without a reason")
	}

	// A nil reader is the same deployment shape as a blank address, and must
	// not panic on the way to saying so.
	if list = NewProbe("dataset-key", "http://router:8081", nil).List(context.Background()); byKey(t, list, "router_runtime").State != StateOff {
		t.Error("a nil router reader should be reported the same way as a missing address")
	}
}

// The healthy deployment: nothing to report, and every reason field empty so
// the console has nothing to render in a list titled "not enabled here".
func TestFullyConfiguredDeploymentReportsEverythingOn(t *testing.T) {
	list := NewProbe("dataset-key", "http://router:8081/", workingRouter()).List(context.Background())

	for _, c := range list {
		if c.State != StateOn {
			t.Errorf("%s state = %q, want %q", c.Key, c.State, StateOn)
		}
		if c.Reason != "" {
			t.Errorf("%s carries reason %q while on", c.Key, c.Reason)
		}
	}
}

// The router answering "acest_enabled: false" is a real negative answer, and
// it is the one case where off is correct — with a reason naming what stops
// working rather than the flag that is false.
func TestRouterReportingACESTDisabledIsOffWithAConsequence(t *testing.T) {
	router := &stubRouter{switches: &bridge.RuntimeSwitches{ACESTEnabled: false}}

	acest := byKey(t, NewProbe("dataset-key", "http://router:8081", router).List(context.Background()), "acest")
	if acest.State != StateOff {
		t.Fatalf("state = %q, want %q", acest.State, StateOff)
	}
	if acest.Reason == "" {
		t.Fatal("off without a reason")
	}
}

// The console renders this list straight down the page. One that reshuffles as
// things break cannot be compared with the one an operator saw yesterday, so
// the order is part of the contract, not an accident of the implementation.
func TestListIsAlwaysTheSameThreeKeysInTheSameOrder(t *testing.T) {
	want := []string{"knowledge_management", "router_runtime", "acest"}

	probes := map[string]*Probe{
		"fully configured":   NewProbe("dataset-key", "http://router:8081", workingRouter()),
		"nothing configured": NewProbe("", "", nil),
		"router failing":     NewProbe("", "http://router:8081", &stubRouter{err: errors.New("i/o timeout")}),
		"never constructed":  nil,
	}

	for name, p := range probes {
		list := p.List(context.Background())
		if list == nil {
			t.Fatalf("%s: List returned nil; callers marshal this straight into JSON", name)
		}
		if len(list) != len(want) {
			t.Fatalf("%s: got %d capabilities, want %d", name, len(list), len(want))
		}
		for i, key := range want {
			if list[i].Key != key {
				t.Errorf("%s: position %d is %q, want %q", name, i, list[i].Key, key)
			}
			if list[i].Title == "" {
				t.Errorf("%s: %q has no title", name, key)
			}
			if list[i].Owner != OwnerAdmin && list[i].Owner != OwnerRouter {
				t.Errorf("%s: %q owner = %q, want admin or router", name, key, list[i].Owner)
			}
			switch list[i].State {
			case StateOn, StateOff, StateUnknown:
			default:
				t.Errorf("%s: %q state = %q, want on/off/unknown", name, key, list[i].State)
			}
		}
	}
}

// The reason a tenant reads must never carry the transport error, because that
// error carries the request URL and the URL is this deployment's internal
// router address. The verbatim cause belongs in Detail, which the tenant-facing
// type does not copy.
func TestTheRouterAddressNeverReachesTheReason(t *testing.T) {
	// The shape http.Client actually produces: the wrapper, the URL, the cause.
	transport := errors.New(`reach router: Get "http://10.4.7.19:8090/configz": ` +
		`dial tcp 10.4.7.19:8090: connect: connection refused`)
	p := NewProbe("dataset-key", "http://10.4.7.19:8090", &stubRouter{err: transport})

	list := p.List(context.Background())
	for _, c := range list {
		if strings.Contains(c.Reason, "10.4.7.19") || strings.Contains(c.Reason, "8090") {
			t.Errorf("%s: the reason carries the deployment's router address: %s", c.Key, c.Reason)
		}
		if strings.Contains(c.Reason, "dial tcp") {
			t.Errorf("%s: the reason carries the raw transport error: %s", c.Key, c.Reason)
		}
	}

	// It is not merely dropped: an operator still has to be able to act on it.
	acest := byKey(t, list, "acest")
	if acest.State != StateUnknown {
		t.Errorf("acest state = %q, want unknown", acest.State)
	}
	if !strings.Contains(acest.Detail, "connection refused") {
		t.Errorf("the operator lost the cause entirely: detail = %q", acest.Detail)
	}
	if acest.Reason == "" {
		t.Error("a tenant is told nothing at all")
	}
}
