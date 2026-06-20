# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /listen-together ./cmd/listen-together

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /listen-together /listen-together
EXPOSE 4040
ENTRYPOINT ["/listen-together"]
