// Package capability answers a question the console has never been able to
// answer: which parts of this product are not working here, and why.
//
// A whole class of configuration in this system disables a feature by being
// empty. An unset dataset API key does not fail loudly — it removes the
// knowledge base from every tenant's reach, silently and platform-wide. The
// tenant opens the page, sees nothing, and has no way to learn that the cause
// is a blank line in a deployment file rather than something they did. The
// probe here turns "the feature vanished" into "the feature is off, and here
// is why", which is the difference between a support ticket and a sentence.
//
// The probe deliberately reports three states, not two. "unknown" exists
// because some of these answers live on another process: when that process
// cannot be reached, the honest report is that we do not know. Rendering an
// unreachable source as "off" would be the same silent lie this package was
// written to remove, only one level up.
package capability

import (
	"context"
	"strings"

	"github.com/kefu/unica/admin/internal/bridge"
)

// Capability states. A consumer should treat StateUnknown as "ask again
// later", never as a synonym for StateOff.
const (
	StateOn      = "on"
	StateOff     = "off"
	StateUnknown = "unknown"
)

// Who can do something about a capability that is off. "admin" means the
// answer is in this service's own configuration; "router" means the switch
// lives in the router and this service is only reporting what it was told.
// Tenants are not shown this field — it tells them nothing they can act on —
// but the platform console uses it to point an operator at the right process.
const (
	OwnerAdmin  = "admin"
	OwnerRouter = "router"
)

// Capability is one feature of the platform and whether it works here.
//
// Reason is empty when State is StateOn: there is nothing to explain about a
// feature that works. For every other state it is a full operator-facing
// sentence naming the consequence, not just the variable that is unset —
// "DIFY_DATASET_API_KEY is empty" tells a tenant nothing, while "uploading and
// viewing segments are unavailable" tells them exactly what they lost.
type Capability struct {
	Key    string `json:"key"`
	Title  string `json:"title"`
	State  string `json:"state"` // "on" | "off" | "unknown"
	Reason string `json:"reason,omitempty"`
	Owner  string `json:"owner"` // "admin" | "router"

	// Detail is the underlying failure, verbatim, for an operator who has to
	// act on it. It is separate from Reason because Reason is shown to tenants
	// and this is not: a transport error carries the request URL, and that URL
	// is this deployment's internal router address. Whoever narrows this struct
	// for a tenant drops this field along with Owner.
	Detail string `json:"detail,omitempty"`
}

// SwitchReader is the part of the router bridge this package needs. It is
// declared here rather than taking *bridge.RouterBridge so a test can supply a
// router that fails, which is the case that matters most: the unreachable path
// is the one where a careless implementation would report "off".
type SwitchReader interface {
	Switches(ctx context.Context) (*bridge.RuntimeSwitches, error)
}

// Probe holds what this deployment was configured with. It stores the values,
// not booleans derived from them, so that the decision about what "configured"
// means stays in one place.
type Probe struct {
	datasetAPIKey string
	routerURL     string
	router        SwitchReader
}

// NewProbe builds a probe from the two configuration values whose emptiness
// silently disables things, plus the router bridge used to ask about the
// switches this service does not own.
//
// Any of the three may be empty or nil: that is not an error, it is precisely
// the deployment shape this package exists to report on.
func NewProbe(datasetAPIKey string, routerURL string, router SwitchReader) *Probe {
	return &Probe{
		datasetAPIKey: strings.TrimSpace(datasetAPIKey),
		routerURL:     strings.TrimRight(strings.TrimSpace(routerURL), "/"),
		router:        router,
	}
}

// List reports every capability this package knows about.
//
// The result is never nil and always carries the same keys in the same order,
// whatever the state of the deployment. A console renders this straight down
// the page, and a list that reshuffles as things break is a list an operator
// cannot compare against the one they saw yesterday.
//
// The router is asked exactly once even though two entries depend on the
// answer: this runs inside a page load, and the bridge's cache should not be
// the only thing standing between a console viewer and two calls to a service
// that may already be timing out.
func (p *Probe) List(ctx context.Context) []Capability {
	if p == nil {
		// A handler may hold a probe that was never constructed. Reporting
		// everything as unconfigured is the truthful reading of that, and it
		// keeps the response shape identical either way.
		p = &Probe{}
	}

	switches, switchErr := p.readSwitches(ctx)

	return []Capability{
		p.knowledgeManagement(),
		p.routerRuntime(switchErr),
		p.acest(switches, switchErr),
	}
}

// readSwitches asks the router for its runtime switches, or explains why it
// did not ask. A missing address is reported through the same error channel as
// a failed call because the consequence for the caller is identical: there is
// no answer, and the reason must be shown rather than replaced by a default.
func (p *Probe) readSwitches(ctx context.Context) (*bridge.RuntimeSwitches, error) {
	if p.routerURL == "" || p.router == nil {
		return nil, errNoRouter
	}
	sw, err := p.router.Switches(ctx)
	if err != nil {
		return nil, err
	}
	if sw == nil {
		// A bridge that returns neither switches nor an error would otherwise
		// be read as "everything the router owns is off".
		return nil, errEmptyRouterAnswer
	}
	return sw, nil
}

type probeError string

func (e probeError) Error() string { return string(e) }

const (
	errNoRouter          = probeError("没有配置 router 地址")
	errEmptyRouterAnswer = probeError("router 返回了空的运行状态")
)

// knowledgeManagement is off when the Dify dataset API key is empty. This is
// the original case for this package: the key is read by the platform and by
// every tenant's knowledge page, and with it blank all of them degrade to an
// empty screen with no error anywhere.
func (p *Probe) knowledgeManagement() Capability {
	c := Capability{
		Key:   "knowledge_management",
		Title: "知识库管理",
		Owner: OwnerAdmin,
		State: StateOn,
	}
	if p.datasetAPIKey == "" {
		c.State = StateOff
		c.Reason = "这个部署没有配置 Dify 数据集 API Key，" +
			"全平台租户的知识库管理已禁用：上传、删除、查看分段均不可用。" +
			"页面上看到的空列表不是「没有内容」，是这条通道根本没接上。"
	}
	return c
}

// routerRuntime is the capability the other router-owned answers hang from.
//
// Without an address it is off, and everything the router decides becomes
// unknowable from here. With an address that cannot be reached it is neither
// on nor off: the feature is configured and may well be running: we simply
// could not ask. Saying "off" there would tell an operator to go and enable
// something that is already enabled.
func (p *Probe) routerRuntime(switchErr error) Capability {
	c := Capability{
		Key:   "router_runtime",
		Title: "router 运行状态",
		Owner: OwnerAdmin,
	}
	switch {
	case p.routerURL == "" || p.router == nil:
		c.State = StateOff
		c.Reason = "这个部署没有配置 router 地址（ROUTER_INTERNAL_URL），" +
			"意图分诊、商业阶段策略、经验库这些由 router 决定的状态都读不到。" +
			"本页不会拿默认值顶替它们——一个看起来合理的错值，会被当成消息真正在按的设定。"
	case switchErr != nil:
		c.State = StateUnknown
		c.Reason = "配置了 router 地址，但这次读不到它的运行状态。" +
			"这不代表功能被关掉，只代表此刻问不到；下面由 router 决定的几项因此也是未知。"
		c.Detail = switchErr.Error()
	default:
		c.State = StateOn
	}
	return c
}

// acest is only knowable through the router, so this entry is where the
// distinction between "off" and "unknown" earns its keep. A failed read must
// never be flattened into "off": that would report a feature as disabled on
// the strength of a network timeout, and an operator acting on it would go
// looking for a switch that is already in the position they wanted.
func (p *Probe) acest(switches *bridge.RuntimeSwitches, switchErr error) Capability {
	c := Capability{
		Key:   "acest",
		Title: "经验库（ACEST）",
		Owner: OwnerRouter,
	}
	switch {
	case switchErr != nil:
		c.State = StateUnknown
		c.Reason = "读不到 router 的运行状态，" +
			"无法判断经验库是否启用。这里不写成「未启用」：读不到的来源不该被渲染成否定答案。"
		c.Detail = switchErr.Error()
	case switches.ACESTEnabled:
		c.State = StateOn
	default:
		c.State = StateOff
		c.Reason = "经验库召回与反馈未装配：机器人不会引用历史处理经验，" +
			"坐席对建议的采纳与否也不会回流成新经验。这一项由 router 决定，改它要动 router 的配置。"
	}
	return c
}
