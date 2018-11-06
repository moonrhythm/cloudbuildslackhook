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
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/acoshift/configfile"
	"google.golang.org/api/option"
	"gopkg.in/yaml.v2"
)

type subscription struct {
	ID        string `yaml:"id"`
	ProjectID string `yaml:"projectId"`
	URL       string `yaml:"url"`
}

var (
	config = configfile.NewReader("config")
)

func main() {
	ctx := context.Background()

	client, err := pubsub.NewClient(ctx, "", option.WithScopes(pubsub.ScopePubSub))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	var subs []subscription
	err = yaml.Unmarshal(config.Bytes("subscriptions"), &subs)
	if err != nil {
		log.Fatal(err)
	}

	for _, sub := range subs {
		fmt.Printf("subscribe to %s/%s\n", sub.ProjectID, sub.ID)
		go client.SubscriptionInProject(sub.ID, sub.ProjectID).Receive(ctx, (&msgHandler{sub.ID, sub.ProjectID, sub.URL}).Handle)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)

	<-stop
}

type msgHandler struct {
	id        string
	projectID string
	url       string
}

func (h *msgHandler) Handle(ctx context.Context, msg *pubsub.Message) {
	fmt.Printf("%s/%s: received message\n", h.projectID, h.id)

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

	images := strings.Join(d.Images, "\n")
	if images == "" {
		images = "-"
	}

	go sendSlackMessage(h.url, &slackMsg{
		Attachments: []slackAttachment{
			{
				Fallback:  fmt.Sprintf("cloudbuild: %s:%s", d.SourceProvenance.ResolvedRepoSource.RepoName, d.SourceProvenance.ResolvedRepoSource.CommitSha),
				Color:     color,
				Title:     "Cloud Build",
				TitleLink: d.LogURL,
				Fields: []slackField{
					{
						Title: "Build ID",
						Value: d.ID,
					},
					{
						Title: "Images",
						Value: images,
					},
					{
						Title: "Repository",
						Value: d.SourceProvenance.ResolvedRepoSource.RepoName,
					},
					{
						Title: "Commit SHA",
						Value: d.SourceProvenance.ResolvedRepoSource.CommitSha,
					},
					{
						Title: "Project ID",
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
}

var statusColor = map[string]string{
	"QUEUED":         "#508dff",
	"WORKING":        "#fffc55",
	"SUCCESS":        "#5bff37",
	"FAILURE":        "#f92a2a",
	"INTERNAL_ERROR": "#f92a2a",
	"TIMEOUT":        "#f92a2a",
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
	Timeout   string   `json:"timeout"`
	Images    []string `json:"images"`
	Artifacts struct {
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

var client = http.Client{
	Timeout: 10 * time.Second,
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
	resp, err := client.Do(req)
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
