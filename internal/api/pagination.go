package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// PageFetcher loads a page beginning at skip with the requested page size.
type PageFetcher func(context.Context, int, int) ([]any, error)

// FetchPages implements zero-based bounded pagination with repeated-page
// detection. A zero limit returns without invoking fetch.
func FetchPages(ctx context.Context, skip int, limit *int, fetchAll bool, fetch PageFetcher) ([]any, error) {
	if skip < 0 || (limit != nil && *limit < 0) {
		return nil, &Error{Type: "validation", Message: "skip and limit must be non-negative"}
	}
	if limit != nil && *limit == 0 {
		return []any{}, nil
	}
	pageSize := 100
	if limit != nil && *limit < pageSize {
		pageSize = *limit
	}
	if !fetchAll {
		page, err := fetch(ctx, skip, pageSize)
		if err != nil {
			return nil, err
		}
		if limit != nil && len(page) > *limit {
			page = page[:*limit]
		}
		return page, nil
	}
	result := make([]any, 0)
	seen := make(map[string]struct{})
	current := skip
	for {
		want := pageSize
		if limit != nil && *limit-len(result) < want {
			want = *limit - len(result)
		}
		if want <= 0 {
			break
		}
		page, err := fetch(ctx, current, want)
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		fingerprintBytes, _ := json.Marshal(page)
		fingerprint := string(fingerprintBytes)
		if _, repeated := seen[fingerprint]; repeated {
			break
		}
		seen[fingerprint] = struct{}{}
		result = append(result, page...)
		if limit != nil && len(result) >= *limit {
			result = result[:*limit]
			break
		}
		if len(page) < want {
			break
		}
		next := current + len(page)
		if next <= current {
			return nil, errors.New("pagination did not advance")
		}
		current = next
		if len(seen) > 10000 {
			return nil, fmt.Errorf("pagination exceeded safety bound")
		}
	}
	return result, nil
}
