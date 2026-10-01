package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const MaxLinks = 30

type Link struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	Ref            string     `json:"ref"`
	URL            string     `json:"url"`
	SubtaskID      string     `json:"subtaskId,omitempty"`
	Role           string     `json:"role"`
	State          string     `json:"state,omitempty"`
	StateCategory  string     `json:"stateCategory,omitempty"`
	StateChangedAt *time.Time `json:"stateChangedAt,omitempty"`
}

// LinkInput deliberately excludes observation timestamps: only the store sets them.
type LinkInput struct {
	ID            string  `json:"id,omitempty"`
	Ref           string  `json:"ref,omitempty"`
	URL           string  `json:"url,omitempty"`
	SubtaskID     string  `json:"subtaskId,omitempty"`
	Role          string  `json:"role,omitempty"`
	State         *string `json:"state,omitempty"`
	StateCategory *string `json:"stateCategory,omitempty"`
}

func (input *LinkInput) UnmarshalJSON(data []byte) error {
	if err := ValidateNoNullFields(data); err != nil {
		return err
	}
	type plain LinkInput
	var value plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	*input = LinkInput(value)
	return nil
}

var jiraKey = regexp.MustCompile(`^[A-Z][A-Z0-9]+-[1-9][0-9]*$`)
var mrRef = regexp.MustCompile(`^([A-Za-z0-9_][A-Za-z0-9_.-]*(?:/[A-Za-z0-9_][A-Za-z0-9_.-]*)+)!([1-9][0-9]*)$`)

var jiraHost = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)

var (
	jiraSiteMu sync.RWMutex
	jiraSite   string
)

// ValidateHostname accepts a bare lowercase hostname: no scheme, port or path.
func ValidateHostname(host string) error {
	if len(host) > 253 || !jiraHost.MatchString(host) {
		return fmt.Errorf("invalid Jira site %q: use a bare hostname such as example.atlassian.net", host)
	}
	return nil
}

// SetJiraSite sets the only Jira host links may point at; an empty host clears it.
func SetJiraSite(host string) error {
	if host != "" {
		if err := ValidateHostname(host); err != nil {
			return err
		}
	}
	jiraSiteMu.Lock()
	jiraSite = host
	jiraSiteMu.Unlock()
	return nil
}

func JiraSite() string {
	jiraSiteMu.RLock()
	defer jiraSiteMu.RUnlock()
	return jiraSite
}

// CanonicalLink accepts only provider identities, never arbitrary fetch targets.
func CanonicalLink(value string) (kind, ref, destination string, err error) {
	invalid := func() (string, string, string, error) {
		return "", "", "", fmt.Errorf("invalid Jira/MR reference %q", value)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return invalid()
	}
	site := JiraSite()
	if jiraKey.MatchString(value) {
		if site == "" {
			return "", "", "", fmt.Errorf("Jira links need a Jira site (TODO_JIRA_SITE): %q", value)
		}
		return "jira", value, "https://" + site + "/browse/" + value, nil
	}
	if m := mrRef.FindStringSubmatch(value); m != nil {
		return "mr", value, "https://gitlab.com/" + m[1] + "/-/merge_requests/" + m[2], nil
	}
	u, e := url.Parse(value)
	if e != nil || u.Scheme != "https" || u.User != nil || u.RawPath != "" || strings.Contains(u.Path, "%") || u.Opaque != "" {
		return invalid()
	}
	switch {
	case site != "" && u.Host == site:
		if strings.HasPrefix(u.Path, "/browse/") && jiraKey.MatchString(strings.TrimPrefix(u.Path, "/browse/")) {
			return CanonicalLink(strings.TrimPrefix(u.Path, "/browse/"))
		}
	case u.Host == "gitlab.com":
		parts := strings.Split(u.Path, "/-/merge_requests/")
		if len(parts) == 2 {
			candidate := strings.TrimPrefix(parts[0], "/") + "!" + parts[1]
			if mrRef.MatchString(candidate) {
				return CanonicalLink(candidate)
			}
		}
	}
	return invalid()
}

func hasSubtask(task Task, id string) bool {
	if id == "" {
		return true
	}
	for _, sub := range task.Subtasks {
		if sub.ID == id {
			return true
		}
	}
	return false
}

func pruneLinks(task *Task) {
	links := make([]Link, 0, len(task.Links))
	for _, link := range task.Links {
		if hasSubtask(*task, link.SubtaskID) {
			links = append(links, link)
		}
	}
	task.Links = links
}

func linkIdentity(link Link) string { return link.Kind + "\x00" + link.Ref + "\x00" + link.SubtaskID }

func (s *Store) normalizeLink(task Task, input LinkInput) (Link, error) {
	value := input.Ref
	if value == "" {
		value = input.URL
	}
	kind, ref, destination, err := CanonicalLink(value)
	if err != nil {
		return Link{}, err
	}
	if input.URL != "" {
		k, r, _, e := CanonicalLink(input.URL)
		if e != nil || k != kind || r != ref {
			return Link{}, errors.New("link ref and URL must identify the same target")
		}
	}
	if !hasSubtask(task, input.SubtaskID) {
		return Link{}, errors.New("link subtaskId does not exist")
	}
	link := Link{Kind: kind, Ref: ref, URL: destination, SubtaskID: input.SubtaskID, Role: "required"}
	for _, old := range task.Links {
		if linkIdentity(old) == linkIdentity(link) {
			link = old
			break
		}
	}
	if input.ID != "" && input.ID != link.ID {
		return Link{}, errors.New("link id does not belong to this identity")
	}
	if input.Role != "" {
		link.Role = input.Role
	}
	if link.Role != "required" && link.Role != "reference" {
		return Link{}, errors.New("link role must be required or reference")
	}
	oldState, oldCategory := link.State, link.StateCategory
	if input.State != nil {
		link.State = *input.State
	}
	if input.StateCategory != nil {
		link.StateCategory = *input.StateCategory
	}
	if kind == "mr" {
		if link.StateCategory != "" {
			return Link{}, errors.New("MR stateCategory must be empty")
		}
		switch link.State {
		case "", "opened", "merged", "closed", "locked":
		default:
			return Link{}, errors.New("invalid MR state")
		}
	} else {
		value, e := validateText("link state", link.State, 100, false)
		if e != nil {
			return Link{}, e
		}
		if value != link.State || strings.IndexFunc(link.State, unicode.IsControl) >= 0 {
			return Link{}, errors.New("Jira state must not contain control characters or surrounding whitespace")
		}
		switch link.StateCategory {
		case "", "new", "indeterminate", "done":
		default:
			return Link{}, errors.New("invalid Jira stateCategory")
		}
		if (link.State == "") != (link.StateCategory == "") {
			return Link{}, errors.New("Jira state and category must both be set or both be empty")
		}
	}
	if oldState != link.State || oldCategory != link.StateCategory {
		now := s.now()
		link.StateChangedAt = &now
	}
	if link.ID == "" {
		id, e := s.randomID(12)
		if e != nil {
			return Link{}, e
		}
		link.ID = "l" + id
	}
	return link, nil
}

func (s *Store) metadata(task *Task, links *[]LinkInput, memo, mode *string) error {
	if task.ReconcileMode == "" {
		task.ReconcileMode = "automatic"
	}
	if mode != nil {
		task.ReconcileMode = *mode
	}
	if task.ReconcileMode != "automatic" && task.ReconcileMode != "manual" {
		return errors.New("reconcileMode must be automatic or manual")
	}
	if memo != nil {
		value, err := validateText("agentContext", *memo, 4000, false)
		if err != nil {
			return err
		}
		task.AgentContext = value
	}
	if links != nil {
		if len(*links) > MaxLinks {
			return fmt.Errorf("links cannot exceed %d", MaxLinks)
		}
		result := make([]Link, 0, len(*links))
		seen := map[string]bool{}
		for _, input := range *links {
			link, err := s.normalizeLink(*task, input)
			if err != nil {
				return err
			}
			key := linkIdentity(link)
			if seen[key] {
				return errors.New("duplicate link identity")
			}
			seen[key] = true
			result = append(result, link)
		}
		task.Links = result
	} else {
		pruneLinks(task)
	}
	return nil
}

func (s *Store) upsertLink(task *Task, input LinkInput) (Link, error) {
	link, err := s.normalizeLink(*task, input)
	if err != nil {
		return Link{}, err
	}
	for i, old := range task.Links {
		if old.ID == link.ID {
			if !reflect.DeepEqual(old, link) {
				task.Links[i] = link
				task.UpdatedAt = s.now()
			}
			return link, nil
		}
	}
	if len(task.Links) >= MaxLinks {
		return Link{}, fmt.Errorf("links cannot exceed %d", MaxLinks)
	}
	task.Links = append(task.Links, link)
	task.UpdatedAt = s.now()
	return link, nil
}

func removeLink(task *Task, id string) error {
	for i, link := range task.Links {
		if link.ID == id {
			task.Links = append(task.Links[:i], task.Links[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) LinkTask(id string, input LinkInput, options ...Mutation) (Link, uint64, error) {
	var link Link
	var candidate map[string]Task
	err := s.transact(func(tasks map[string]Task) error {
		candidate = tasks
		task, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		var e error
		link, e = s.upsertLink(&task, input)
		if e == nil {
			tasks[id] = task
		}
		return e
	}, options...)
	if err != nil {
		return Link{}, 0, err
	}
	return cloneTask(Task{Links: []Link{link}}).Links[0], candidate[id].Revision, nil
}

func (s *Store) UnlinkTask(id, linkID string, options ...Mutation) (uint64, error) {
	var candidate map[string]Task
	err := s.transact(func(tasks map[string]Task) error {
		candidate = tasks
		task, ok := tasks[id]
		if !ok {
			return ErrNotFound
		}
		if err := removeLink(&task, linkID); err != nil {
			return err
		}
		task.UpdatedAt = s.now()
		tasks[id] = task
		return nil
	}, options...)
	if err != nil {
		return 0, err
	}
	return candidate[id].Revision, nil
}

func linksJSON(links []Link) string {
	if links == nil {
		links = []Link{}
	}
	data, _ := json.Marshal(links)
	return string(data)
}

// validateStoredMetadata must not generate IDs, timestamps or history.
func validateStoredMetadata(task Task) error {
	if task.ReconcileMode != "automatic" && task.ReconcileMode != "manual" {
		return errors.New("invalid stored reconcileMode")
	}
	if _, err := validateText("agentContext", task.AgentContext, 4000, false); err != nil {
		return err
	}
	if len(task.Links) > MaxLinks {
		return errors.New("stored links exceed limit")
	}
	identities, ids := map[string]bool{}, map[string]bool{}
	for _, link := range task.Links {
		if link.ID == "" || ids[link.ID] || identities[linkIdentity(link)] {
			return errors.New("empty or duplicate stored link identity/id")
		}
		ids[link.ID] = true
		identities[linkIdentity(link)] = true
		kind, ref, destination, err := CanonicalLink(link.Ref)
		if err != nil || kind != link.Kind || ref != link.Ref || destination != link.URL {
			return errors.New("noncanonical stored link")
		}
		// The existing identity prevents ID generation; unchanged state prevents a clock call.
		var validator Store
		if _, err := validator.normalizeLink(task, LinkInput{ID: link.ID, Ref: link.Ref, Role: link.Role, SubtaskID: link.SubtaskID}); err != nil {
			return err
		}
	}
	return nil
}
