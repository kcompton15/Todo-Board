package reconcile

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/kcompton15/Todo-Board/internal/store"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type JiraClient struct {
	Client  *http.Client
	Command Commander
	Origin  string
	Email   string
	Token   func(context.Context) (string, error)
}

func (j *JiraClient) token(ctx context.Context) (string, error) {
	if j.Token != nil {
		return j.Token(ctx)
	}
	data, err := j.Command.Run(ctx, Command{Name: "/usr/bin/security", Args: []string{"find-generic-password", "-a", j.Email, "-s", "atlassian-api-token", "-w"}})
	if err != nil {
		return "", errors.New("Jira Keychain token unavailable")
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return "", errors.New("Jira Keychain token empty")
	}
	return token, nil
}
func (j *JiraClient) Issues(ctx context.Context, keys []string) (map[string]IssueState, error) {
	result := map[string]IssueState{}
	if len(keys) == 0 {
		return result, nil
	}
	if j.Email == "" {
		return result, ErrConfigMissing
	}
	token, err := j.token(ctx)
	if err != nil {
		return result, err
	}
	origin := j.Origin
	if origin == "" {
		return result, ErrConfigMissing
	}
	client := http.Client{Timeout: 20 * time.Second}
	if j.Client != nil {
		client = *j.Client
		if client.Timeout <= 0 {
			client.Timeout = 20 * time.Second
		}
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return errors.New("Jira redirect refused") }
	for offset := 0; offset < len(keys); offset += 50 {
		end := offset + 50
		if end > len(keys) {
			end = len(keys)
		}
		batch := keys[offset:end]
		allowed := map[string]bool{}
		for _, key := range batch {
			kind, ref, _, err := store.CanonicalLink(key)
			if err != nil || kind != "jira" || ref != key {
				return result, errors.New("invalid Jira key")
			}
			allowed[key] = true
		}
		cursor := ""
		seen := map[string]bool{}
		complete := false
		for page := 0; page < 100; page++ {
			query := url.Values{"jql": {"key in (" + strings.Join(batch, ",") + ")"}, "fields": {"status"}, "maxResults": {"100"}}
			if cursor != "" {
				query.Set("nextPageToken", cursor)
			}
			var data []byte
			for attempt := 0; attempt < 3; attempt++ {
				req, err := http.NewRequestWithContext(ctx, "GET", origin+"/rest/api/3/search/jql?"+query.Encode(), nil)
				if err != nil {
					return result, errors.New("invalid Jira request")
				}
				req.SetBasicAuth(j.Email, token)
				response, err := client.Do(req)
				if err != nil {
					return result, errors.New("Jira request failed")
				}
				body, readErr := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
				response.Body.Close()
				if readErr != nil || len(body) > 4<<20 {
					return result, errors.New("Jira response read/size failure")
				}
				if response.StatusCode == 429 || response.StatusCode == 503 {
					if attempt == 2 {
						return result, errors.New("Jira retry limit")
					}
					delay := time.Duration(attempt+1) * time.Second
					if raw := response.Header.Get("Retry-After"); raw != "" {
						if seconds, e := strconv.Atoi(raw); e == nil {
							delay = time.Duration(seconds) * time.Second
						} else if when, e := http.ParseTime(raw); e == nil {
							delay = time.Until(when)
						}
					}
					if delay < 0 {
						delay = 0
					}
					if delay > 10*time.Second {
						return result, errors.New("Jira Retry-After exceeds run budget")
					}
					select {
					case <-ctx.Done():
						return result, ctx.Err()
					case <-time.After(delay):
					}
					continue
				}
				if response.StatusCode != 200 {
					return result, errors.New("Jira search unavailable")
				}
				data = body
				break
			}
			var out struct {
				Issues []struct {
					Key    string `json:"key"`
					Fields struct {
						Status struct {
							Name     string `json:"name"`
							Category struct {
								Key string `json:"key"`
							} `json:"statusCategory"`
						} `json:"status"`
					} `json:"fields"`
				} `json:"issues"`
				Next   string `json:"nextPageToken"`
				IsLast bool   `json:"isLast"`
			}
			if json.Unmarshal(data, &out) != nil || out.Issues == nil {
				return result, errors.New("invalid Jira search response")
			}
			for _, issue := range out.Issues {
				category := issue.Fields.Status.Category.Key
				if !allowed[issue.Key] || issue.Fields.Status.Name == "" || (category != "done" && category != "new" && category != "indeterminate") {
					return result, errors.New("unexpected Jira identity or status")
				}
				if _, ok := result[issue.Key]; ok {
					return result, errors.New("duplicate Jira issue")
				}
				result[issue.Key] = IssueState{Key: issue.Key, Status: issue.Fields.Status.Name, Category: category}
			}
			if out.IsLast {
				complete = true
				break
			}
			if out.Next == "" || seen[out.Next] {
				return result, errors.New("incomplete Jira pagination")
			}
			seen[out.Next] = true
			cursor = out.Next
		}
		if !complete {
			return result, errors.New("Jira pagination limit")
		}
	}
	return result, nil
}
