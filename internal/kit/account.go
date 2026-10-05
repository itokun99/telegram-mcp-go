package kit

// This file ports runtime.with_account and get_client: account label
// resolution and the read-only fan-out.

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// CallFunc is one account's tool body for Router.Route. It receives the
// account label to run against ("" means "the sole account" in single-account
// mode) and returns either a string result or a []any of content items.
type CallFunc func(ctx context.Context, account string) (any, error)

// Router mirrors runtime.with_account over the configured account labels.
// Labels keep their configured order, which the fan-out string uses.
type Router struct {
	labels []string
}

// NewRouter builds a Router over labels in their configured (discovery)
// order.
func NewRouter(labels []string) *Router {
	return &Router{labels: append([]string(nil), labels...)}
}

// Labels returns a copy of the configured account labels.
func (r *Router) Labels() []string { return append([]string(nil), r.labels...) }

// Multi reports whether more than one account is configured.
func (r *Router) Multi() bool { return len(r.labels) > 1 }

// Label resolves an account label the way runtime.get_client does: an
// explicit label is matched case-insensitively; an empty label selects the
// sole account in single-account mode and is an error in multi-account mode.
func (r *Router) Label(account string) (string, error) {
	if account == "" {
		if len(r.labels) == 1 {
			return r.labels[0], nil
		}
		return "", fmt.Errorf("Account is required. Available accounts: %s", strings.Join(r.labels, ", "))
	}
	label := strings.ToLower(account)
	for _, known := range r.labels {
		if known == label {
			return label, nil
		}
	}
	return "", fmt.Errorf("Unknown account '%s'. Available accounts: %s", account, strings.Join(r.labels, ", "))
}

// Route implements the with_account dispatch:
//
//   - single-account mode or an explicit account: call once, no tagging;
//   - multi-account, no account, write: return the "account is required"
//     error string without calling anything;
//   - multi-account, no account, read-only: call every account concurrently
//     and prefix each result with its label (joined with blank lines when
//     all results are strings, flattened into a []any otherwise).
func (r *Router) Route(ctx context.Context, account string, readonly bool, call CallFunc) (any, error) {
	if account != "" || !r.Multi() {
		return call(ctx, account)
	}
	if !readonly {
		return fmt.Sprintf("Error: 'account' is required. Available accounts: %s", strings.Join(r.labels, ", ")), nil
	}

	results := make([]any, len(r.labels))
	errs := make([]error, len(r.labels))
	var wg sync.WaitGroup
	for i, label := range r.labels {
		wg.Add(1)
		go func(i int, label string) {
			defer wg.Done()
			results[i], errs[i] = call(ctx, label)
		}(i, label)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return fanOutJoin(r.labels, results), nil
}

// fanOutJoin mirrors the result assembly of with_account: all-string results
// join as "[label]\nresult" blocks separated by blank lines; otherwise each
// account contributes its label marker followed by its content items.
func fanOutJoin(labels []string, results []any) any {
	allStrings := true
	for _, result := range results {
		if _, ok := result.(string); !ok {
			allStrings = false
			break
		}
	}
	if allStrings {
		parts := make([]string, len(labels))
		for i, label := range labels {
			parts[i] = fmt.Sprintf("[%s]\n%s", label, results[i].(string))
		}
		return strings.Join(parts, "\n\n")
	}

	out := make([]any, 0, len(labels)*2)
	for i, label := range labels {
		out = append(out, "["+label+"]")
		if items, ok := results[i].([]any); ok {
			out = append(out, items...)
			continue
		}
		out = append(out, results[i])
	}
	return out
}
