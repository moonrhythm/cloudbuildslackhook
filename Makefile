default: clean build docker

clean:
	rm -f entrypoint

build:
	env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o entrypoint -ldflags '-w -s' main.go

docker:
	docker build -t acoshift/cloudbuildslackhook .
	docker push acoshift/cloudbuildslackhook
