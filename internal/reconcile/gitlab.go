package reconcile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kcompton15/Todo-Board/internal/store"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type GitLabClient struct {
	Command    Commander
	Username   string
	Group      string
	ReviewerID int
}

func (g *GitLabClient) get(ctx context.Context, path string, paginate bool) ([]byte, error) {
	args := []string{"api", "--method", "GET", "--hostname", "gitlab.com"}
	if paginate {
		args = append(args, "--paginate")
	}
	args = append(args, path)
	return g.Command.Run(ctx, Command{Name: "glab", Args: args})
}
func decodePages[T any](data []byte) ([]T, error) {
	result := []T{}
	decoder := json.NewDecoder(bytes.NewReader(data))
	for page := 0; page <= 100; page++ {
		var values []T
		err := decoder.Decode(&values)
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil || values == nil {
			return nil, errors.New("invalid paginated GitLab response")
		}
		if page == 100 {
			return nil, errors.New("GitLab pagination limit")
		}
		result = append(result, values...)
	}
	return nil, errors.New("GitLab pagination limit")
}

type gitlabMR struct {
	IID          int    `json:"iid"`
	State        string `json:"state"`
	Title        string `json:"title"`
	SourceBranch string `json:"source_branch"`
	WebURL       string `json:"web_url"`
}

func parseMR(raw gitlabMR) (MRState, error) {
	kind, ref, _, err := store.CanonicalLink(raw.WebURL)
	if err != nil || kind != "mr" {
		return MRState{}, errors.New("invalid GitLab MR identity")
	}
	split := strings.LastIndex(ref, "!")
	iid, err := strconv.Atoi(ref[split+1:])
	if err != nil || iid != raw.IID {
		return MRState{}, errors.New("GitLab IID mismatch")
	}
	switch raw.State {
	case "opened", "closed", "merged", "locked":
	default:
		return MRState{}, errors.New("unknown GitLab state")
	}
	return MRState{Project: ref[:split], IID: iid, State: raw.State, Title: raw.Title, SourceBranch: raw.SourceBranch, WebURL: raw.WebURL}, nil
}
func mrPath(project string, iid int) string {
	return "projects/" + url.PathEscape(project) + "/merge_requests/" + strconv.Itoa(iid)
}
func (g *GitLabClient) MR(ctx context.Context, project string, iid int) (MRState, error) {
	data, err := g.get(ctx, mrPath(project, iid), false)
	if err != nil {
		return MRState{}, err
	}
	var raw gitlabMR
	if json.Unmarshal(data, &raw) != nil {
		return MRState{}, errors.New("invalid GitLab MR response")
	}
	mr, err := parseMR(raw)
	if err == nil && (mr.Project != project || mr.IID != iid) {
		err = errors.New("GitLab returned different MR")
	}
	return mr, err
}
func (g *GitLabClient) SearchMRs(ctx context.Context, key string) ([]MRState, error) {
	kind, _, _, err := store.CanonicalLink(key)
	if err != nil || kind != "jira" {
		return nil, errors.New("search requires exact Jira key")
	}
	if g.Group == "" {
		return nil, nil
	}
	data, err := g.get(ctx, "groups/"+url.PathEscape(g.Group)+"/merge_requests?search="+url.QueryEscape(key)+"&in=title&state=all&scope=all&per_page=100", true)
	if err != nil {
		return nil, err
	}
	raw, err := decodePages[gitlabMR](data)
	if err != nil {
		return nil, err
	}
	boundary := regexp.MustCompile(`(^|[^A-Za-z0-9_-])` + regexp.QuoteMeta(key) + `([^A-Za-z0-9_-]|$)`)
	result := []MRState{}
	seen := map[string]bool{}
	for _, item := range raw {
		if !boundary.MatchString(item.Title) {
			continue
		}
		mr, err := parseMR(item)
		if err != nil {
			return nil, err
		}
		if !seen[mr.WebURL] {
			result = append(result, mr)
			seen[mr.WebURL] = true
		}
	}
	return result, nil
}

type gitlabUser struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
	Bot      bool   `json:"bot"`
}

func (g *GitLabClient) reviewer(ctx context.Context) (int, error) {
	if g.ReviewerID != 0 {
		return g.ReviewerID, nil
	}
	if !gitlabUsernamePattern.MatchString(g.Username) {
		return 0, ErrConfigMissing
	}
	data, err := g.get(ctx, "users?username="+url.QueryEscape(g.Username), false)
	if err != nil {
		return 0, err
	}
	var users []gitlabUser
	if json.Unmarshal(data, &users) != nil || len(users) != 1 || users[0].Username != g.Username || users[0].ID == 0 || users[0].Bot {
		return 0, errors.New("configured reviewer identity unavailable")
	}
	g.ReviewerID = users[0].ID
	return g.ReviewerID, nil
}

type gitlabNote struct {
	ID        int        `json:"id"`
	Author    gitlabUser `json:"author"`
	CreatedAt *time.Time `json:"created_at"`
	System    bool       `json:"system"`
	Type      string     `json:"type"`
	Body      string     `json:"body"`
}

func (g *GitLabClient) ReviewActivity(ctx context.Context, project string, iid int) (ReviewActivity, error) {
	reviewer, err := g.reviewer(ctx)
	if err != nil {
		return ReviewActivity{}, err
	}
	result := ReviewActivity{ReviewerID: reviewer}
	prefix := mrPath(project, iid)
	data, err := g.get(ctx, prefix+"/approvals", false)
	if err != nil {
		return result, err
	}
	var approvals struct {
		ApprovedBy []struct {
			User       gitlabUser `json:"user"`
			ApprovedAt *time.Time `json:"approved_at"`
		} `json:"approved_by"`
	}
	if json.Unmarshal(data, &approvals) != nil || approvals.ApprovedBy == nil {
		return result, errors.New("invalid approval response")
	}
	data, err = g.get(ctx, prefix+"/discussions?per_page=100", true)
	if err != nil {
		return result, err
	}
	type discussion struct {
		ID    string       `json:"id"`
		Notes []gitlabNote `json:"notes"`
	}
	discussions, err := decodePages[discussion](data)
	if err != nil {
		return result, err
	}
	webURL := "https://gitlab.com/" + project + "/-/merge_requests/" + strconv.Itoa(iid)
	seen := map[int]bool{}
	for _, d := range discussions {
		for _, n := range d.Notes {
			if n.ID == 0 || n.Author.ID == 0 || n.CreatedAt == nil {
				return result, errors.New("incomplete published note identity")
			}
			if seen[n.ID] {
				continue
			}
			seen[n.ID] = true
			result.Events = append(result.Events, ReviewEvent{ID: fmt.Sprintf("note:%d", n.ID), URL: fmt.Sprintf("%s#note_%d", webURL, n.ID), AuthorID: n.Author.ID, CreatedAt: n.CreatedAt, Kind: "comment", Body: n.Body, System: n.System, Bot: n.Author.Bot, Diff: n.Type == "DiffNote"})
		}
	}
	for _, a := range approvals.ApprovedBy {
		if a.User.ID != reviewer || a.User.Bot {
			continue
		}
		at := a.ApprovedAt
		id := fmt.Sprintf("approval:%d", reviewer)
		link := webURL
		if at == nil {
			for _, event := range result.Events {
				if event.AuthorID == reviewer && event.System && event.Body == "approved this merge request" && event.CreatedAt != nil && (at == nil || event.CreatedAt.After(*at)) {
					at = event.CreatedAt
					id = event.ID
					link = event.URL
				}
			}
		}
		result.Events = append(result.Events, ReviewEvent{ID: id, URL: link, AuthorID: reviewer, CreatedAt: at, Kind: "approval"})
	}
	result.Complete = true
	return result, nil
}
