package replay

import "github.com/kefu/unica/pkg/domain"

func violationWithEvidence(evidence string) domain.Violation {
	return domain.Violation{Kind: domain.ViolationDenied, Evidence: evidence}
}
