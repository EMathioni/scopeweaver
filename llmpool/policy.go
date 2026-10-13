package llmpool

import "regexp"

var policyCodePattern = regexp.MustCompile(`(?:^|[^a-zA-Z0-9_])(?:cyber_policy|misalignment_policy_violation)(?:$|[^a-zA-Z0-9_])`)

// IsPolicyFailure identifies explicit provider safeguard codes in the error
// representation used by Norma. A policy block is terminal for this request;
// replaying it or routing it to another model is not availability recovery.
func IsPolicyFailure(err error) bool {
	return err != nil && policyCodePattern.MatchString(err.Error())
}
