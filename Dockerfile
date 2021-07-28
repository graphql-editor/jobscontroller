FROM golang:latest as build

ADD . /app
ENV CGO_ENABLED=0
WORKDIR /app
RUN go build ./cmd/jobscontroller

FROM alpine:3.14 as tini

ENV TINI_VERSION=v0.19.0
	
ADD https://github.com/krallin/tini/releases/download/${TINI_VERSION}/tini-static-amd64 /tini
RUN chmod +x /tini

FROM gcr.io/distroless/static:latest

COPY --from=tini /tini /tini
ENTRYPOINT ["/tini", "--"]

COPY --from=build /app/jobscontroller /jobscontroller
CMD ["/jobscontroller"]
