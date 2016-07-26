package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"github.com/nebril/go-jira"
)

var githubClient *github.Client
var jiraClient *jira.Client

type Configuration struct {
	JIRAUsername                 string
	JIRAPassword                 string
	JIRAHost                     string
	GitHubLinkFieldName          string
	GitHubLinkFieldID            string
	GitHubLabelsRelevantToSearch []string
	GitHubUsers                  []string
	GitHubPreloadRepoName        string
	GitHubPreloadRepoOwner       string
	GitHubUsername               string
	GitHubPassword               string
	GitHubJIRAUserMapping        map[string]string
	TimeForCreatingJIRATicket    int64
	JIRATeamID                   string
	JIRABoardID                  string
	JIRAProjectKey               string
	JIRANewIssueType             string
}

var config Configuration

func getOpenPRIssuesByPeople(people []string, owner, repository string, labels []string) ([]github.Issue, error) {

	opt := &github.IssueListByRepoOptions{
		Creator:     "",
		Labels:      labels,
		ListOptions: github.ListOptions{PerPage: 100, Page: 1},
	}

	allPulls := make([]github.Issue, 0)

	for _, user := range people {
		opt.Creator = user
		opt.ListOptions.Page = 1
		for {
			page, resp, err := githubClient.Issues.ListByRepo(owner, repository, opt)

			if err != nil {
				fmt.Printf("\nerror: %v\n", err)
				return nil, err
			}
			allPulls = append(allPulls, page...)

			if resp.NextPage == 0 {
				break
			}
			opt.ListOptions.Page = resp.NextPage
		}
	}

	return allPulls, nil
}

func getOpenPRsByPeople(people []string, owner, repository string) ([]github.PullRequest, error) {
	opt := &github.PullRequestListOptions{
		ListOptions: github.ListOptions{PerPage: 100, Page: 1},
	}

	allPulls := make([]github.PullRequest, 0)

	for {
		page, resp, err := githubClient.PullRequests.List(owner, repository, opt)

		if err != nil {
			fmt.Printf("\nerror: %v\n", err)
			return nil, err
		}
		for _, pull := range page {
			for _, user := range people {
				if *pull.User.Login == user {
					allPulls = append(allPulls, pull)
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opt.ListOptions.Page = resp.NextPage
	}

	return allPulls, nil
}

func getPRByLink(link string, pullRequestsPreloaded []github.PullRequest) (*github.PullRequest, error) {
	var found github.PullRequest
	for _, pr := range pullRequestsPreloaded {
		if strings.Trim(*pr.HTMLURL, "/") == strings.Trim(link, "/") {
			found = pr
			return &found, nil
		}
	}

	owner, repo, id, err := getURLParts(link)
	if err != nil {
		return nil, err
	}

	pr, _, err := githubClient.PullRequests.Get(owner, repo, id)

	if err != nil {
		return nil, err
	}

	return pr, nil
}

func getOpenPRTickets() ([]jira.Issue, error) {
	jql := fmt.Sprintf("%s != \"\" AND status != \"Done\" AND status != \"In QA\"", config.GitHubLinkFieldName)
	results := 50
	opt := &jira.SearchOptions{StartAt: 0, MaxResults: results}
	allIssues := make([]jira.Issue, 0)

	for {
		page, _, err := jiraClient.Issue.Search(jql, opt)
		if err != nil {
			return nil, err
		}
		allIssues = append(allIssues, page...)
		//if fewer than max results were returned - there are no more, break
		if len(page) < results {
			break
		}
		opt.StartAt = opt.StartAt + results
	}
	return allIssues, nil
}

func initiateClients() error {
	tp := github.BasicAuthTransport{
		Username: config.GitHubUsername,
		Password: config.GitHubPassword,
	}
	githubClient = github.NewClient(tp.Client())

	jc, err := jira.NewClient(nil, config.JIRAHost)
	if err != nil {
		return err
	}

	var res bool
	res, err = jc.Authentication.AcquireSessionCookie(config.JIRAUsername, config.JIRAPassword)
	if err != nil || res == false {
		return err
	}
	jiraClient = jc
	return err
}

func changeTicketStatusBasedOnPR(ticket jira.Issue, issuesPreloaded []github.Issue, pullRequestsPreloaded []github.PullRequest, linkChannel chan string, wg *sync.WaitGroup) {
	defer wg.Done()
	link, err := getPRLink(ticket)
	if err != nil {
		fmt.Println(err)
		return
	}
	linkChannel <- link

	pr, err := getPRByLink(link, pullRequestsPreloaded)
	if err != nil {
		fmt.Printf("%s Failed to load PR: \"%s\"\n", ticket.Key, err)
		return
	}

	transitions, _, err := jiraClient.Transition.GetList(ticket.ID)
	if err != nil {
		fmt.Println(err)
		return
	}

	if isDone(pr) {
		if isTicketDone(&ticket) {
			return
		} else {
			fields := map[string]jira.TransitionField{
				"resolution": jira.TransitionField{
					Name: "Done",
				},
			}
			err = changeTicketStatus(&ticket, "Done", transitions, fields)
		}
	} else if isClosed(pr) {
		if isTicketDone(&ticket) {
			return
		} else {
			fields := map[string]jira.TransitionField{
				"resolution": jira.TransitionField{
					Name: "Won't Do",
				},
			}
			err = changeTicketStatus(&ticket, "Done", transitions, fields)
		}
	} else if isReviewed(pr, issuesPreloaded) {
		if isTicketReviewed(&ticket) {
			return
		} else {
			err = changeTicketStatus(&ticket, "Ready to Merge", transitions, nil)
		}
	} else if !isTicketInProgress(&ticket) {
		err = changeTicketStatus(&ticket, "Start Development", transitions, nil)
	}
	if err != nil {
		fmt.Println(err)
	}
	return
}

func getPRLink(ticket jira.Issue) (string, error) {
	customFields, _, err := jiraClient.Issue.GetCustomFields(ticket.ID)
	if err != nil {
		return "", err
	}

	return customFields[config.GitHubLinkFieldID], nil
}

func isClosed(pr *github.PullRequest) bool {
	return pr.ClosedAt != nil
}

func isDone(pr *github.PullRequest) bool {
	if pr.Merged == nil {
		return pr.MergedAt != nil
	}
	return *pr.Merged
}

func isTicketDone(ticket *jira.Issue) bool {
	return ticket.Fields.Status.Name == "Done" || ticket.Fields.Status.Name == "In QA"
}

func isReviewed(pr *github.PullRequest, issuesPreloaded []github.Issue) bool {
	var err error
	var found *github.Issue
	for _, issue := range issuesPreloaded {
		if issue.Number == pr.Number {
			found = &issue
			break
		}
	}

	if found == nil {
		found, err = getIssueFromPR(pr)
		if err != nil {
			fmt.Println(err)
			return false
		}
	}

	for _, label := range found.Labels {
		if *label.Name == "lgtm" { //TODO: Move this to config
			return true
		}
	}
	return false
}

func isTicketReviewed(ticket *jira.Issue) bool {
	return ticket.Fields.Status.Name == "Ready to Merge"
}

func isTicketInProgress(ticket *jira.Issue) bool {
	return ticket.Fields.Status.Name == "In Development"
}

// Create new JIRA transition for issue, based on transition name provided in `status` parameter and preloaded transitions
func changeTicketStatus(ticket *jira.Issue, status string, transitions []jira.Transition, fields map[string]jira.TransitionField) error {
	fmt.Printf("Changing %s status to %s with fields %s\n", ticket.Key, status, fields)
	var found *jira.Transition

	for _, transition := range transitions {
		if status == transition.Name {
			found = &transition
			break
		}
	}

	if found == nil {
		return fmt.Errorf("Transition for %s to %s not found", ticket.Key, status)
	}

	jiraClient.Transition.Create(ticket.ID, found.ID, fields)

	return nil
}

func getIssueFromPR(pr *github.PullRequest) (*github.Issue, error) {
	owner, repo, id, err := getURLParts(*pr.HTMLURL)
	if err != nil {
		return nil, err
	}
	issue, _, err := githubClient.Issues.Get(owner, repo, id)
	return issue, err
}

func initConfig(path string) error {
	file, err := os.Open("config.json")
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&config)
	if err != nil {
		return err
	}
	return nil
}

//Retrieves owner, repository name and issue/PR number from GitHub URL
func getURLParts(link string) (string, string, int, error) {
	u, err := url.Parse(link)
	if err != nil {
		return "", "", 0, err
	}

	elements := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(elements) != 4 {
		return "", "", 0, errors.New(fmt.Sprintf(
			"Path to PR has wrong number of elements. Expected 4, got %v in %v.",
			len(elements), u.Path))
	}
	owner := elements[0]
	repo := elements[1]
	id, err := strconv.Atoi(elements[3])
	if err != nil {
		return "", "", 0, err
	}
	return owner, repo, id, nil
}

//Generate JIRA issues for pull requests without ticket that are older than time specified in config
func generateJIRAIssues(tickets []jira.Issue, pulls []github.PullRequest, linkedPRLinks []string) error {
	sprints, _, err := jiraClient.Sprint.GetList(config.JIRABoardID)
	if err != nil {
		return err
	}
	var activeSprint jira.Sprint

	for _, sprint := range sprints {
		if sprint.State == "active" {
			activeSprint = sprint
			break
		}
	}

	var wg sync.WaitGroup
	for _, pr := range pulls {
		isLinked := false
		for _, link := range linkedPRLinks {
			if strings.Trim(*pr.HTMLURL, "/") == strings.Trim(link, "/") {
				isLinked = true
				break
			}
		}
		if !isLinked && isOldEnough(&pr) {
			wg.Add(1)
			go createNewJIRAIssueFromPR(pr, &activeSprint, &wg)
		}
	}
	wg.Wait()
	return nil
}

//Checks if the pull request was created long enough ago to create new JIRA ticket
func isOldEnough(pr *github.PullRequest) bool {
	return time.Now().Unix()-pr.CreatedAt.Unix() > config.TimeForCreatingJIRATicket
}

func createNewJIRAIssueFromPR(pr github.PullRequest, sprint *jira.Sprint, wg *sync.WaitGroup) {
	defer wg.Done()
	jql := fmt.Sprintf("%s = \"%s\"", config.GitHubLinkFieldName, *pr.HTMLURL)
	opt := &jira.SearchOptions{StartAt: 0, MaxResults: 1}

	issues, _, err := jiraClient.Issue.Search(jql, opt)
	if err != nil {
		fmt.Printf("Could not check for existing issue, not creating new one:%s\n", err)
	}
	if len(issues) > 0 {
		fmt.Printf("Found existing issue for %s: %s, not creating new one\n", *pr.HTMLURL, issues[0].Key)
		return
	}

	//using overriden Issue type from issue_override.go
	i := &Issue{Fields: &IssueFields{
		Type:        jira.IssueType{Name: config.JIRANewIssueType},
		Project:     jira.Project{Key: config.JIRAProjectKey},
		Summary:     *pr.Title,
		Description: *pr.Body,
		GH_PR_link:  *pr.HTMLURL,
		Assignee:    &jira.User{Name: config.GitHubJIRAUserMapping[*pr.User.Login]},
		Team:        Team{ID: config.JIRATeamID},
	}}

	issue, resp, err := CreateWithGH_PR_link(jiraClient, i)
	if err != nil {
		buf := new(bytes.Buffer)
		buf.ReadFrom(resp.Body)
		fmt.Println("Could not create issue: ", err, buf.String())
		return
	}
	fmt.Printf("Created %s for %s\n", issue.Key, *pr.HTMLURL)

	_, err = jiraClient.Sprint.AddIssuesToSprint(sprint.ID, []string{issue.Key})
	if err != nil {
		fmt.Println("Could not move issue to sprint:", err)
	}
}

func main() {
	err := initConfig("config.json")
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}

	err = initiateClients()
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}

	tickets, err := getOpenPRTickets()
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}

	ourIssues, err := getOpenPRIssuesByPeople(config.GitHubUsers, config.GitHubPreloadRepoOwner, config.GitHubPreloadRepoName, config.GitHubLabelsRelevantToSearch)

	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}

	ourPulls, err := getOpenPRsByPeople(config.GitHubUsers, config.GitHubPreloadRepoOwner, config.GitHubPreloadRepoName)
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}

	var wg sync.WaitGroup
	linkChan := make(chan string)
	wg.Add(len(tickets))
	for _, ticket := range tickets {
		go changeTicketStatusBasedOnPR(ticket, ourIssues, ourPulls, linkChan, &wg)
	}

	links := make([]string, len(tickets))
	for index, _ := range tickets {
		links[index] = <-linkChan
	}
	err = generateJIRAIssues(tickets, ourPulls, links)
	wg.Wait()
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}
}
