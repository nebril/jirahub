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
	u, err := url.Parse(link)
	if err != nil {
		fmt.Printf("\nerror: %v\n", err)
		return nil, err
	}

	elements := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(elements) != 4 {
		return nil, errors.New(fmt.Sprintf(
			"Path to PR has wrong number of elements. Expected 4, got %v in %v.", len(elements), u.Path))
	}
	owner := elements[0]
	repo := elements[1]
	id, err := strconv.Atoi(elements[3])
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
	jql := fmt.Sprintf("%s != \"\"", config.GitHubLinkFieldName)
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
	githubClient = github.NewClient(nil)

	jc, err := jira.NewClient(nil, config.JIRAHost)
	if err != nil {
		return err
	}

	var res bool
	res, err = jc.Authentication.AcquireSessionCookie(config.JIRAUsername, config.JIRAPassword) //TODO: move user/pass to config file
	if err != nil || res == false {
		return err
	}
	jiraClient = jc
	return err
}

func changeTicketStatusBasedOnPR(ticket jira.Issue, issuesPreloaded []github.Issue, wg *sync.WaitGroup) error {
	defer wg.Done()
	link, err := getPRLink(ticket)
	fmt.Println(link)
	if err != nil {
		return err
	}

	pr, err := getPRByLink(link)
	if err != nil {
		return err
	}

	if isDone(pr) {
		//TODO: change ticket state to done
		return nil
	} else if isReviewed(pr, issuesPreloaded) {
		//TODO: change ticket state to "ready to merge"
		return nil
	} else {
		//TODO: change ticket state to "in progress"
		return nil
	}
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

func isReviewed(pr *github.PullRequest, issuesPreloaded []github.Issue) bool {
	var found *github.Issue
	for _, issue := range issuesPreloaded {
		if issue.Number == pr.Number {
			found = &issue
			break
		}
	}

	if found == nil {
		return false //TODO: possibly PR from other repo, retrieve it from github
	}

	for _, label := range found.Labels {
		if strings.Compare(*label.Name, "lgtm") == 0 { //TODO: Move this to config
			return true
		}
	}
	return false
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

	ourIssues, err := getOpenPRsByPeople(config.GitHubUsers, config.GitHubPreloadRepoOwner, config.GitHubPreloadRepoName, config.GitHubUsers)
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
