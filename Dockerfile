FROM acoshift/go-scratch

ADD entrypoint /entrypoint

RUN ["./entrypoint"]
