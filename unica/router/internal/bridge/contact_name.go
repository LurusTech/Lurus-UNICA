package bridge

import "strings"

// ContactName is the name a customer's Chatwoot contact is given: the channel
// the customer wrote in and the last six characters of its platform user id,
// "微信公众号 客户 a1b2c3". Contacts used to be named by their identifier — a
// customer UUID from the handoff, an OpenID from the forwarder — which an
// agent can neither read nor tell apart. Both paths name contacts here so they
// cannot drift apart again.
func ContactName(channelName, platformUserID string) string {
	name := "客户"
	if channel := strings.TrimSpace(channelName); channel != "" {
		name = channel + " " + name
	}
	if id := []rune(strings.TrimSpace(platformUserID)); len(id) > 0 {
		if len(id) > 6 {
			id = id[len(id)-6:]
		}
		name += " " + string(id)
	}
	return name
}
