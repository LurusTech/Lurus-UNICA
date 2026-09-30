package facts

import "github.com/kefu/unica/pkg/domain"

// friendlyCheckYAMLError turns a domain.ParseYAML failure into the wording
// checkYAML hands back to the portal. The translation itself lives in
// domain.FriendlyYAMLError — this package only decides that checkYAML's
// callers (the /validate and /publish handlers) always see the translated
// form, never the raw yaml.v3/Go error a business operator cannot act on.
func friendlyCheckYAMLError(err error) string {
	return domain.FriendlyYAMLError(err)
}
