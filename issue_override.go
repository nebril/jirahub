package main

import "github.com/nebril/go-jira"

type Issue struct {
	Expand string       `json:"expand,omitempty"`
	ID     string       `json:"id,omitempty"`
	Self   string       `json:"self,omitempty"`
	Key    string       `json:"key,omitempty"`
	Fields *IssueFields `json:"fields,omitempty"`
}

type IssueFields struct {
	Type              jira.IssueType     `json:"issuetype"`
	Project           jira.Project       `json:"project,omitempty"`
	Resolution        *jira.Resolution   `json:"resolution,omitempty"`
	Priority          *jira.Priority     `json:"priority,omitempty"`
	Resolutiondate    string             `json:"resolutiondate,omitempty"`
	Created           string             `json:"created,omitempty"`
	Watches           *jira.Watches      `json:"watches,omitempty"`
	Assignee          *jira.User         `json:"assignee,omitempty"`
	Updated           string             `json:"updated,omitempty"`
	Description       string             `json:"description,omitempty"`
	Summary           string             `json:"summary"`
	Creator           *jira.User         `json:"Creator,omitempty"`
	Reporter          *jira.User         `json:"reporter,omitempty"`
	Components        []*jira.Component  `json:"components,omitempty"`
	Status            *jira.Status       `json:"status,omitempty"`
	Progress          *jira.Progress     `json:"progress,omitempty"`
	AggregateProgress *jira.Progress     `json:"aggregateprogress,omitempty"`
	Worklog           *jira.Worklog      `json:"worklog,omitempty"`
	IssueLinks        []*jira.IssueLink  `json:"issuelinks,omitempty"`
	Comments          []*jira.Comment    `json:"comment.comments,omitempty"`
	FixVersions       []*jira.FixVersion `json:"fixVersions,omitempty"`
	Labels            []string           `json:"labels,omitempty"`
	Subtasks          []*jira.Subtasks   `json:"subtasks,omitempty"`
	Attachments       []*jira.Attachment `json:"attachment,omitempty"`
	// Field added for custom field containing GH_PR_link. Need to be consistent with GitHubLinkFieldID in config.json
	GH_PR_link string `json:"customfield_22000,omitempty"`
	Team       Team   `json:"customfield_19000,omitempty"`
}

type Team struct {
	ID string `json:"id,omitempty"`
}

func CreateWithGH_PR_link(c *jira.Client, issue *Issue) (*Issue, *jira.Response, error) {
	apiEndpoint := "rest/api/2/issue/"
	req, err := c.NewRequest("POST", apiEndpoint, issue)
	if err != nil {
		return nil, nil, err
	}

	responseIssue := new(Issue)
	resp, err := c.Do(req, responseIssue)
	if err != nil {
		return nil, resp, err
	}

	return responseIssue, resp, nil
}
