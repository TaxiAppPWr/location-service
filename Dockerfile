FROM golang:1.24.1-bookworm AS build

WORKDIR /uber-clone

COPY go.mod ./
COPY go.sum ./

RUN go mod download && go mod verify

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -o /uber-clone/location_service .

FROM gcr.io/distroless/static-debian12:latest

WORKDIR /uber-clone

COPY --from=build /uber-clone/location_service /uber-clone/location_service

EXPOSE 8080

ENTRYPOINT ["/uber-clone/location_service", "go run"]