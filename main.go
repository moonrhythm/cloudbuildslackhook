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
		buildID := msg.Attributes["buildId"]
		status := msg.Attributes["status"]
		m := fmt.Sprintf("cloudbuild: %s - %s - https://console.cloud.google.com/gcr/builds/%s?project=%s", buildID, status, buildID, projectID)
		go sendSlackMessage(slackURL, m)
		msg.Ack()
	})
	if err != nil {
		log.Fatal(err)
	}
}

func sendSlackMessage(slackURL string, message string) error {
	if slackURL == "" {
		return nil
	}

	payload := struct {
		Text string `json:"text"`
	}{message}
	buf := bytes.Buffer{}
	err := json.NewEncoder(&buf).Encode(&payload)
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
