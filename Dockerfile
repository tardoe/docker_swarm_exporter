FROM golang:1.19 as builder

WORKDIR /build

# Let's cache modules retrieval - those don't change so often
COPY go.mod .
COPY go.sum .
RUN go mod download

COPY . .

RUN go build .

WORKDIR /dist
RUN cp /build/docker_swarm_exporter ./docker_swarm_exporter

FROM ubuntu:jammy
RUN apt update && apt install -y wget

COPY --chown=0:0 --from=builder /dist /
CMD [ "/docker_swarm_exporter" ]

EXPOSE 9675/tcp

HEALTHCHECK --interval=30s --timeout=20s --start-period=10s --retries=3 CMD [ "wget --spider http://localhost:9675/status || exit 1" ]
