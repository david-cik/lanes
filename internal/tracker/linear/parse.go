package linear

import (
	"encoding/json"
	"time"

	"github.com/david-cik/lanes/internal/tracker"
)

// closed state types are hidden from the board.
var closed = map[string]bool{"completed": true, "canceled": true, "duplicate": true}

type page struct {
	HasNextPage bool   `json:"hasNextPage"`
	Cursor      string `json:"cursor"`
}

// parseIssues decodes one list_issues page, dropping closed issues.
func parseIssues(b []byte) (issues []tracker.Issue, next string, err error) {
	var r struct {
		page
		Issues []struct {
			ID            string    `json:"id"`
			Title         string    `json:"title"`
			URL           string    `json:"url"`
			GitBranchName string    `json:"gitBranchName"`
			Status        string    `json:"status"`
			StatusType    string    `json:"statusType"`
			Team          string    `json:"team"`
			UpdatedAt     time.Time `json:"updatedAt"`
		} `json:"issues"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, "", err
	}
	for _, i := range r.Issues {
		if closed[i.StatusType] {
			continue
		}
		issues = append(issues, tracker.Issue{
			Key: i.ID, Title: i.Title, URL: i.URL, Team: i.Team,
			State: i.Status, StateType: i.StatusType, BranchName: i.GitBranchName,
			UpdatedAt: i.UpdatedAt,
		})
	}
	return issues, r.next(), nil
}

// parseUsers decodes one list_users page, dropping deactivated users.
func parseUsers(b []byte) (users []tracker.User, next string, err error) {
	var r struct {
		page
		Users []struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Email    string `json:"email"`
			IsActive bool   `json:"isActive"`
		} `json:"users"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, "", err
	}
	for _, u := range r.Users {
		if u.IsActive {
			users = append(users, tracker.User{ID: u.ID, Name: u.Name, Email: u.Email})
		}
	}
	return users, r.next(), nil
}

func (p page) next() string {
	if p.HasNextPage {
		return p.Cursor
	}
	return ""
}
