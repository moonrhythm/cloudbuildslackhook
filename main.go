package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/acoshift/configfile"
	"google.golang.org/api/option"
)

func main() {
	config := configfile.NewReader("config")

	projectID := config.String("project_id")
	slackURL := config.String("slack_url")

	client, err := pubsub.NewClient(
		context.Background(),
		projectID,
		option.WithCredentialsFile("./config/service_account.json"),
		option.WithScopes(pubsub.ScopePubSub),
	)
	if err != nil {
		log.Fatal(err)
	}
	err = client.Subscription(config.String("subscription")).Receive(context.Background(), func(ctx context.Context, msg *pubsub.Message) {
		defer msg.Ack()

		var d buildData
		err := json.Unmarshal(msg.Data, &d)
		if err != nil {
			return
		}

		color := statusColor[d.Status]
		if color == "" {
			return
		}

		images := strings.Join(d.Images, ", ")
		go sendSlackMessage(slackURL, &slackMsg{
			Attachments: []slackAttachment{
				{
					Fallback:  fmt.Sprintf("cloudbuild: %s:%s", d.SourceProvenance.ResolvedRepoSource.RepoName, d.SourceProvenance.ResolvedRepoSource.CommitSha),
					Color:     color,
					Title:     "Cloud Build",
					TitleLink: fmt.Sprintf("https://console.cloud.google.com/gcr/builds/%s?project=%s", d.ID, d.SourceProvenance.ResolvedRepoSource.ProjectID),
					Text:      images,
					Fields: []slackField{
						{
							Title: "Name",
							Value: d.SourceProvenance.ResolvedRepoSource.ProjectID,
						},
						{
							Title: "RepoName",
							Value: d.SourceProvenance.ResolvedRepoSource.RepoName,
						},
						{
							Title: "CommitSha",
							Value: d.SourceProvenance.ResolvedRepoSource.CommitSha,
						},
						{
							Title: "ProjectID",
							Value: d.SourceProvenance.ResolvedRepoSource.ProjectID,
						},
						{
							Title: "Status",
							Value: d.Status,
						},
					},
				},
			},
		})
	})
	if err != nil {
		log.Fatal(err)
	}
}

var statusColor = map[string]string{
	// "QUEUED":         "#b2b2b2",
	"WORKING":        "#faff77",
	"SUCCESS":        "#75ff56",
	"FAILURE":        "#f92a2a",
	"INTERNAL_ERROR": "#a51c1c",
	"TIMEOUT":        "#820202",
	"CANCELLED":      "#b959ff",
}

type buildData struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Status    string `json:"status"`
	Source    struct {
		RepoSource struct {
			ProjectID  string `json:"projectId"`
			RepoName   string `json:"repoName"`
			BranchName string `json:"branchName"`
		} `json:"repoSource"`
	} `json:"source"`
	CreateTime time.Time `json:"createTime"`
	Timeout    string    `json:"timeout"`
	Images     []string  `json:"images"`
	Artifacts  struct {
		Images []string `json:"images"`
	} `json:"artifacts"`
	LogsBucket       string `json:"logsBucket"`
	SourceProvenance struct {
		ResolvedRepoSource struct {
			ProjectID string `json:"projectId"`
			RepoName  string `json:"repoName"`
			CommitSha string `json:"commitSha"`
		} `json:"resolvedRepoSource"`
	} `json:"sourceProvenance"`
	BuildTriggerID string `json:"buildTriggerId"`
	LogURL         string `json:"logUrl"`
}

type slackMsg struct {
	Text        string            `json:"text,omitempty"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

type slackAttachment struct {
	Fallback   string       `json:"fallback"`
	Color      string       `json:"color"`
	Pretext    string       `json:"pretext"`
	AuthorName string       `json:"author_name,omitempty"`
	AnthorLink string       `json:"anthor_link,omitempty"`
	AuthorIcon string       `json:"anthor_icon,omitempty"`
	Title      string       `json:"title"`
	TitleLink  string       `json:"title_link"`
	Text       string       `json:"text"`
	Fields     []slackField `json:"fields"`
	ImageURL   string       `json:"image_url,omitempty"`
	ThumbURL   string       `json:"thumb_url,omitempty"`
	Footer     string       `json:"footer,omitempty"`
	FooterIcon string       `json:"footer_icon,omitempty"`
	Timestamp  int64        `json:"ts"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short"`
}

func sendSlackMessage(slackURL string, message *slackMsg) error {
	if slackURL == "" {
		return nil
	}

	buf := bytes.Buffer{}
	err := json.NewEncoder(&buf).Encode(message)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, slackURL, &buf)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	io.Copy(ioutil.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("response not ok")
	}
	return nil
}
