package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/acoshift/configfile"
	"google.golang.org/api/option"
)

type subscription struct {
	ID        string `yaml:"id"`
	ProjectID string `yaml:"projectId"`
	URL       string `yaml:"url"`
}

var (
	config = configfile.NewReader("config")
	subs   = make(map[string]*subscription) // projects/%s/subscriptions/%s
)

func main() {
	// id|project|slackUrl,id|project|slackUrl
	subList := strings.Split(strings.TrimSpace(config.String("subscriptions")), ",")
	for _, sub := range subList {
		x := strings.Split(sub, "|")
		if len(x) != 3 {
			continue
		}
		key := fmt.Sprintf("projects/%s/subscriptions/%s", x[1], x[0])
		subs[key] = &subscription{
			ID:        x[0],
			ProjectID: x[1],
			URL:       x[2],
		}
		fmt.Println("load", key)
	}

	mode := config.String("mode")
	if mode == "push" {
		startPush(config.StringDefault("port", "8080"))
		return
	}

	startPull()
}

func startPull() {
	ctx := context.Background()

	client, err := pubsub.NewClient(ctx, "", option.WithScopes(pubsub.ScopePubSub))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	for key, sub := range subs {
		fmt.Printf("subscribe to %s\n", key)
		go client.SubscriptionInProject(sub.ID, sub.ProjectID).
			Receive(ctx, msgHandler{sub}.Handle)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)

	<-stop
}

type msgHandler struct {
	*subscription
}

func (h msgHandler) Handle(ctx context.Context, msg *pubsub.Message) {
	defer msg.Ack()

	fmt.Printf("received message from %s/%s\n", h.ProjectID, h.ID)

	var d buildData
	err := json.Unmarshal(msg.Data, &d)
	if err != nil {
		return
	}

	err = processBuildData(h.URL, &d)
	if err != nil {
		msg.Nack()
		return
	}
}

func startPush(port string) {
	badRequest := func(w http.ResponseWriter) {
		http.Error(w, "Bad Request", http.StatusBadRequest)
	}

	fmt.Println("Listening on", port)
	http.ListenAndServe(":"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			badRequest(w)
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "application/json" {
			badRequest(w)
			return
		}

		var msg struct {
			Message struct {
				Data []byte `json:"data,omitempty"`
				ID   string `json:"id"`
			} `json:"message"`
			Subscription string `json:"subscription"`
		}
		err := json.NewDecoder(r.Body).Decode(&msg)
		if err != nil {
			log.Println(err)
			badRequest(w)
			return
		}

		if msg.Subscription == "" || msg.Message.ID == "" {
			badRequest(w)
			return
		}

		fmt.Printf("received message from %s\n", msg.Subscription)

		// lookup subscription
		sub := subs[msg.Subscription]
		if sub == nil {
			log.Println("subscription not found", msg.Subscription)
			badRequest(w)
			return
		}

		var d buildData
		err = json.Unmarshal(msg.Message.Data, &d)
		if err != nil {
			log.Println("invalid message body")
			badRequest(w)
			return
		}

		processBuildData(sub.URL, &d)
	}))
}

func processBuildData(slackURL string, d *buildData) error {
	color := statusColor[d.Status]
	if color == "" {
		return nil
	}

	images := strings.Join(d.Images, "\n")
	if images == "" {
		images = "-"
	}

	return sendSlackMessage(slackURL, &slackMsg{
		Attachments: []slackAttachment{
			{
				Fallback: fmt.Sprintf("cloudbuild: %s:%s",
					d.SourceProvenance.ResolvedRepoSource.RepoName,
					d.SourceProvenance.ResolvedRepoSource.CommitSha,
				),
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
	AuthorLink string       `json:"author_link,omitempty"`
	AuthorIcon string       `json:"author_icon,omitempty"`
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
	Timeout: 5 * time.Second,
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
