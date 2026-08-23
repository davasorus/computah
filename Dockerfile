# computah runs against an external OpenAI-compatible model server; the image
# is just the static binary. Point it at your server with --url on run.
FROM gcr.io/distroless/static-debian12:nonroot
COPY computah /usr/local/bin/computah
ENTRYPOINT ["computah"]
