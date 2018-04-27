FROM acoshift/go-scratch

ADD entrypoint /entrypoint

CMD ["./entrypoint"]
