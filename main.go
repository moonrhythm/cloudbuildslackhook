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
	"strings"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/acoshift/configfile"
	"google.golang.org/api/option"
)

var (
	config   = configfile.NewReader("config")
	mode     = config.String("mode") // push, pull
	slackURL = config.String("slack_url")
)

func main() {
	if mode == "push" {
		port := config.StringDefault("port", "8080")
		startPush(port)
		return
	}

	startPull()
}

func startPull() {
	projectID := config.String("project_id")
	subscription := config.String("subscription")
	googCredJSON := config.Bytes("google_application_credentials_json")

	ctx := context.Background()
	opt := []option.ClientOption{option.WithScopes(pubsub.ScopePubSub)}
	if len(googCredJSON) > 0 {
		opt = append(opt, option.WithCredentialsJSON(googCredJSON))
	}

	client, err := pubsub.NewClient(ctx, projectID, opt...)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	fmt.Printf("subscribe to %s/%s\n", projectID, subscription)
	err = client.Subscription(subscription).
		Receive(ctx, func(ctx context.Context, msg *pubsub.Message) {
			defer msg.Ack()

			log.Println("received message")
			log.Println(string(msg.Data))

			var d buildData
			err := json.Unmarshal(msg.Data, &d)
			if err != nil {
				return
			}

			err = processBuildData(&d)
			if err != nil {
				msg.Nack()
				return
			}
		})
	if err != nil {
		log.Fatal(err)
		return
	}
}

func startPush(port string) {
	fmt.Println("Listening on", port)
	http.ListenAndServe(":"+port, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)

		if r.Method != http.MethodPost {
			return
		}
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if mt != "application/json" {
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
			return
		}

		if msg.Subscription == "" {
			log.Println("invalid message")
			return
		}

		log.Printf("received push message from %s\n", msg.Subscription)

		var d buildData
		err = json.Unmarshal(msg.Message.Data, &d)
		if err != nil {
			log.Println("invalid message body")
			return
		}

		processBuildData(&d)
	}))
}

func processBuildData(d *buildData) error {
	color := statusColor[d.Status]
	if color == "" {
		return nil
	}

	fields := []slackField{
		{
			Title: "Build ID",
			Value: d.ID,
		},
	}

	if images := strings.Join(d.Images, "\n"); images != "" {
		fields = append(fields, slackField{
			Title: "Images",
			Value: images,
		})
	}

	fallbackText := "cloudbuild: "

	if repoName := d.Substitutions.RepoName; repoName != "" {
		fields = append(fields, slackField{
			Title: "Repository",
			Value: d.SourceProvenance.ResolvedRepoSource.RepoName,
		})
		fallbackText += repoName

		if commitSHA := d.Substitutions.CommitSHA; commitSHA != "" {
			fields = append(fields, slackField{
				Title: "Commit SHA",
				Value: commitSHA,
			})
			fallbackText += ":" + commitSHA
		}
	} else if bucketName := d.SourceProvenance.ResolvedStorageSource.Bucket; bucketName != "" {
		fields = append(fields, slackField{
			Title: "Bucket",
			Value: bucketName,
		})
		fallbackText += repoName

		if object := d.SourceProvenance.ResolvedStorageSource.Object; object != "" {
			fields = append(fields, slackField{
				Title: "Object",
				Value: object,
			})
			fallbackText += "/" + object
		}
	}

	fields = append(fields, slackField{
		Title: "Project ID",
		Value: d.ProjectID,
	})
	fields = append(fields, slackField{
		Title: "Status",
		Value: d.Status,
	})

	return sendSlackMessage(&slackMsg{
		Attachments: []slackAttachment{
			{
				Fallback: fallbackText,
				Color:     color,
				Title:     "Cloud Build",
				TitleLink: d.LogURL,
				Fields: fields,
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
		StorageSource struct {
			Bucket string `json:"bucket"`
			Object string `json:"object"`
		} `json:"storageSource"`
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
		ResolvedStorageSource struct {
			Bucket string `json:"bucket"`
			Object string `json:"object"`
		} `json:"resolvedStorageSource"`
	} `json:"sourceProvenance"`
	BuildTriggerID string `json:"buildTriggerId"`
	LogURL         string `json:"logUrl"`
	Substitutions struct {
		// Github App source will be storage source but still have repo detail inside substitutions
		RepoName string `json:"REPO_NAME"`
		BranchName string `json:"BRANCH_NAME"`
		CommitSHA string `json:"COMMIT_SHA"`
		RevisionID string `json:"REVISION_ID"`
		ShortSHA string `json:"SHORT_SHA"`
	} `json:"substitutions"`
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

func sendSlackMessage(message *slackMsg) error {
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
