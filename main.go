package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"

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
}

var config Configuration

func getOpenPRsByPeople(people []string, owner, repository string, labels []string) ([]github.Issue, error) {

	opt := &github.IssueListByRepoOptions{
		Creator:     "",
		Labels:      labels,
		ListOptions: github.ListOptions{PerPage: 100, Page: 1},
	}

	allPulls := make([]github.Issue, 0)

	for _, user := range people {
		opt.Creator = user
		opt.ListOptions.Page = 1
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

	return allPulls, nil
}

func getPRByLink(link string) (*github.PullRequest, error) {
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
	//jql := "key = \"MCP-624\""
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

func changeTicketStatusBasedOnPR(ticket jira.Issue, issuesPreloaded []github.Issue, wg *sync.WaitGroup) {
	defer wg.Done()
	link, err := getPRLink(ticket)
	if err != nil {
		fmt.Println(err)
		return
	}

	pr, err := getPRByLink(link)
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
			err = changeTicketStatus(&ticket, "Done", transitions)
		}
	} else if isReviewed(pr, issuesPreloaded) {
		if isTicketReviewed(&ticket) {
			return
		} else {
			err = changeTicketStatus(&ticket, "Ready to Merge", transitions)
		}
	} else if !isTicketInProgress(&ticket) {
		if ticket.Fields.Type.Name == "Bug" {
			err = changeTicketStatus(&ticket, "In Progress", transitions)
		} else if ticket.Fields.Type.Name == "User Story" {
			err = changeTicketStatus(&ticket, "Start Development", transitions)
		} else {
			err = fmt.Errorf("%s Wrong issue type found: %s", ticket.Key, ticket.Fields.Type.Name)
		}
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

func isDone(pr *github.PullRequest) bool {
	return *pr.Merged
}

func isTicketDone(ticket *jira.Issue) bool {
	return ticket.Fields.Status.Name == "Done" || ticket.Fields.Status.Name == "In QA"
}

func isReviewed(pr *github.PullRequest, issuesPreloaded []github.Issue) bool {
	var err error
	var found *github.Issue
	for _, issue := range issuesPreloaded {
		fmt.Println("Comparing", issue.Number, "with", pr.Number)
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
	return ticket.Fields.Status.Name == "In Development" || ticket.Fields.Status.Name == "In Progress"
}

// Create new JIRA transition for issue, based on transition name provided in `status` parameter and preloaded transitions
func changeTicketStatus(ticket *jira.Issue, status string, transitions []jira.Transition) error {
	fmt.Printf("Changing %s status to %s\n", ticket.Key, status)
	var found *jira.Transition

	for _, transition := range transitions {
		if status == transition.Name {
			found = &transition
			break
		}
	}

	jiraClient.Transition.Create(ticket.ID, found.ID)

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

	//TODO: preload also PRs
	ourIssues, err := getOpenPRsByPeople(config.GitHubUsers, config.GitHubPreloadRepoOwner, config.GitHubPreloadRepoName, config.GitHubLabelsRelevantToSearch)
	if err != nil {
		fmt.Printf("\n%v\n", err)
		return
	}
	var wg sync.WaitGroup
	wg.Add(len(tickets))
	for _, ticket := range tickets {
		go changeTicketStatusBasedOnPR(ticket, ourIssues, &wg)
	}
	wg.Wait()
}
