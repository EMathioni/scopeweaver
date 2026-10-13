package llmpool

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

func TestProviderPolicyBlockDoesNotFailOver(t *testing.T) {
	for _, code := range []string{"cyber_policy", "misalignment_policy_violation"} {
		for _, status := range []int{403, 502} {
			for _, stream := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/%d/stream=%t", code, status, stream), func(t *testing.T) {
					block := fmt.Errorf(`openai-responses: status %d: {"error":{"code":%q}}`, status, code)
					a, b := &fakeProv{errs: []error{block}}, okProv("backup", "unexpected")
					reg := NewRegistry(nil, nil)
					pool := New([]*Member{member(1, "primary", 10, a), member(2, "backup", 5, b)}, reg)
					var err error
					if stream {
						_, err = drain(pool.Stream(context.Background(), llm.CompletionRequest{}))
					} else {
						_, _, _, err = pool.Complete(context.Background(), llm.CompletionRequest{})
					}
					if !errors.Is(err, block) || a.calls != 1 || b.calls != 0 || reg.Get(1).Fails != 0 {
						t.Fatalf("policy block must propagate without failover: err=%v primary=%d backup=%d", err, a.calls, b.calls)
					}
				})
			}
		}
	}
}

func TestPolicyCodeDetectionUsesCodeBoundaries(t *testing.T) {
	for _, message := range []string{"cyber_policy", "openai-responses: cyber_policy", `{"code":"cyber_policy"}`, "status 403 misalignment_policy_violation"} {
		if !IsPolicyFailure(errors.New(message)) {
			t.Errorf("missed policy code: %q", message)
		}
	}
	for _, message := range []string{"cyber_policy_unavailable", "not_cyber_policy", "status 403 permission denied", "status 502 gateway timeout"} {
		if IsPolicyFailure(errors.New(message)) {
			t.Errorf("unrelated error classified as policy: %q", message)
		}
	}
	if IsPolicyFailure(nil) {
		t.Fatal("nil classified as policy")
	}
}
