FROM gcr.io/moonrhythm-containers/go-scratch

ADD entrypoint /entrypoint

CMD ["./entrypoint"]
