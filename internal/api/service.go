package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Service maps user-visible operations to public Kaiten REST resources.
type Service struct{ Client *Client }

func NewService(client *Client) *Service { return &Service{Client: client} }

func (s *Service) ListSpaces(ctx context.Context) (any, bool, error) {
	return s.Client.CachedJSON(ctx, "spaces", "/spaces", nil)
}

func (s *Service) GetSpace(ctx context.Context, id int64) (any, error) {
	return s.Client.JSON(ctx, http.MethodGet, fmt.Sprintf("/spaces/%d", id), nil, nil)
}

func (s *Service) ListBoards(ctx context.Context, spaceID *int64, limit *int) (any, bool, error) {
	if spaceID != nil {
		path := fmt.Sprintf("/spaces/%d/boards", *spaceID)
		key := fmt.Sprintf("boards:space:%d", *spaceID)
		query := url.Values{}
		if limit != nil {
			query.Set("limit", strconv.Itoa(*limit))
			key += ":limit:" + strconv.Itoa(*limit)
		}
		return s.Client.CachedJSON(ctx, key, path, query)
	}
	key := "boards:all"
	if limit != nil {
		key += ":limit:" + strconv.Itoa(*limit)
	}
	return s.Client.cache.Get(ctx, key, func(loadCtx context.Context) (any, error) {
		spaces, _, err := s.ListSpaces(loadCtx)
		if err != nil {
			return nil, err
		}
		boards := make([]any, 0)
		for _, space := range resourcesFrom(spaces, false) {
			value, err := s.Client.JSON(loadCtx, http.MethodGet, fmt.Sprintf("/spaces/%d/boards", space.ID), nil, nil)
			if err != nil {
				return nil, err
			}
			items, ok := value.([]any)
			if !ok {
				return nil, &Error{Type: "upstream", Message: "Kaiten returned a non-array board list"}
			}
			boards = append(boards, items...)
			if limit != nil && len(boards) >= *limit {
				return boards[:*limit], nil
			}
		}
		return boards, nil
	})
}

func (s *Service) GetBoard(ctx context.Context, id int64, includeCards bool) (any, error) {
	value, err := s.Client.JSON(ctx, http.MethodGet, fmt.Sprintf("/boards/%d", id), nil, nil)
	if err != nil {
		return nil, err
	}
	if !includeCards {
		if object, ok := value.(map[string]any); ok {
			delete(object, "cards")
		}
	}
	return value, nil
}

func (s *Service) BoardStructure(ctx context.Context, selector string) (any, bool, error) {
	boards, cached, err := s.ListBoards(ctx, nil, nil)
	if err != nil {
		return nil, false, err
	}
	resource, err := Resolve(selector, resourcesFrom(boards, false), false)
	if err != nil {
		return nil, false, err
	}
	board, err := s.GetBoard(ctx, resource.ID, false)
	if err != nil {
		return nil, false, err
	}
	object, _ := board.(map[string]any)
	return map[string]any{"board": board, "columns": object["columns"], "lanes": object["lanes"]}, cached, nil
}

func (s *Service) GetCard(ctx context.Context, id int64, comments, members, relations bool) (any, error) {
	query := url.Values{}
	var additional []string
	if comments {
		additional = append(additional, "comments")
	}
	if members {
		additional = append(additional, "members")
	}
	if relations {
		additional = append(additional, "children", "parents")
	}
	if len(additional) > 0 {
		query.Set("additional_card_fields", strings.Join(additional, ","))
	}
	return s.Client.JSON(ctx, http.MethodGet, fmt.Sprintf("/cards/%d", id), query, nil)
}

func (s *Service) ListCards(ctx context.Context, query url.Values, skip int, limit *int, fetchAll bool) (any, error) {
	return FetchPages(ctx, skip, limit, fetchAll, func(pageCtx context.Context, offset, pageSize int) ([]any, error) {
		pageQuery := cloneValues(query)
		pageQuery.Set("offset", strconv.Itoa(offset))
		pageQuery.Set("limit", strconv.Itoa(pageSize))
		value, err := s.Client.JSON(pageCtx, http.MethodGet, "/cards", pageQuery, nil)
		if err != nil {
			return nil, err
		}
		items, ok := value.([]any)
		if !ok {
			return nil, &Error{Type: "upstream", Message: "Kaiten returned a non-array card list"}
		}
		return items, nil
	})
}

func (s *Service) CurrentUser(ctx context.Context) (any, error) {
	return s.Client.JSON(ctx, http.MethodGet, "/users/current", nil, nil)
}

func (s *Service) ListUsers(ctx context.Context, spaceID *int64, skip, limit *int) (any, bool, error) {
	query := url.Values{}
	key := "users"
	if spaceID != nil {
		query.Set("space_id", strconv.FormatInt(*spaceID, 10))
		key += ":space:" + strconv.FormatInt(*spaceID, 10)
	}
	if skip != nil {
		query.Set("offset", strconv.Itoa(*skip))
		key += ":offset:" + strconv.Itoa(*skip)
	}
	if limit != nil {
		query.Set("limit", strconv.Itoa(*limit))
		key += ":limit:" + strconv.Itoa(*limit)
	}
	path := "/users"
	if spaceID != nil {
		path = fmt.Sprintf("/spaces/%d/users", *spaceID)
	}
	return s.Client.CachedJSON(ctx, key, path, query)
}

func (s *Service) ResolveUser(ctx context.Context, selector string) (Resource, bool, error) {
	users, cached, err := s.ListUsers(ctx, nil, nil, nil)
	if err != nil {
		return Resource{}, false, err
	}
	resource, err := Resolve(selector, resourcesFrom(users, true), true)
	return resource, cached, err
}

func (s *Service) ResolveBoard(ctx context.Context, selector string) (Resource, bool, error) {
	boards, cached, err := s.ListBoards(ctx, nil, nil)
	if err != nil {
		return Resource{}, false, err
	}
	resource, err := Resolve(selector, resourcesFrom(boards, false), false)
	return resource, cached, err
}

func (s *Service) ResolveColumn(ctx context.Context, boardID int64, selector string) (Resource, error) {
	board, err := s.GetBoard(ctx, boardID, false)
	if err != nil {
		return Resource{}, err
	}
	object, ok := board.(map[string]any)
	if !ok {
		return Resource{}, &Error{Type: "upstream", Message: "Kaiten returned an invalid board object"}
	}
	return Resolve(selector, resourcesFrom(object["columns"], false), false)
}

func (s *Service) ResolveLane(ctx context.Context, boardID int64, selector string) (Resource, error) {
	board, err := s.GetBoard(ctx, boardID, false)
	if err != nil {
		return Resource{}, err
	}
	object, ok := board.(map[string]any)
	if !ok {
		return Resource{}, &Error{Type: "upstream", Message: "Kaiten returned an invalid board object"}
	}
	return Resolve(selector, resourcesFrom(object["lanes"], false), false)
}

func (s *Service) Discovery(ctx context.Context, path, key string) (any, bool, error) {
	query := url.Values{}
	if path == "/company/custom-properties" {
		query.Set("include_values", "true")
	}
	return s.Client.CachedJSON(ctx, key, path, query)
}

func (s *Service) GetPath(ctx context.Context, path string) (any, error) {
	return s.Client.JSON(ctx, http.MethodGet, path, nil, nil)
}

func (s *Service) Mutate(ctx context.Context, method, path string, body any) (any, error) {
	return s.Client.JSON(ctx, method, path, nil, body)
}

func resourcesFrom(value any, user bool) []Resource {
	items, ok := value.([]any)
	if !ok {
		if object, objectOK := value.(map[string]any); objectOK {
			for _, key := range []string{"data", "items", "users", "boards"} {
				if candidate, candidateOK := object[key].([]any); candidateOK {
					items = candidate
					ok = true
					break
				}
			}
		}
	}
	if !ok {
		return nil
	}
	result := make([]Resource, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := integerField(object, "id")
		if id <= 0 {
			continue
		}
		name := stringField(object, "name")
		if name == "" {
			name = stringField(object, "title")
		}
		resource := Resource{ID: id, Name: name}
		if user {
			resource.Username = stringField(object, "username")
			resource.FullName = stringField(object, "full_name")
			resource.Email = stringField(object, "email")
			if resource.Name == "" {
				resource.Name = resource.FullName
			}
		}
		result = append(result, resource)
	}
	return result
}

func integerField(object map[string]any, key string) int64 {
	switch value := object[key].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case int:
		return int64(value)
	case string:
		parsed, _ := strconv.ParseInt(value, 10, 64)
		return parsed
	default:
		return 0
	}
}

func stringField(object map[string]any, key string) string {
	value, _ := object[key].(string)
	return value
}

func cloneValues(values url.Values) url.Values {
	result := make(url.Values, len(values))
	for key, entries := range values {
		result[key] = append([]string(nil), entries...)
	}
	return result
}
